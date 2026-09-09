package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/SyneHQ/apollo/proto"
	"github.com/SyneHQ/apollo/runner"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"strings"
	"testing"
)

type fakeAuthority struct{ member bool }

func (f fakeAuthority) Member(context.Context, string, string, bool) (bool, error) {
	return f.member, nil
}
func (f fakeAuthority) Owner(context.Context, string, bool) (string, error) { return "team-a", nil }
func (f fakeAuthority) Connection(_ context.Context, id, team string) (bool, error) {
	return id == "connection-a" && team == "team-a", nil
}
func encoded(v any) string { raw, _ := json.Marshal(v); return base64.StdEncoding.EncodeToString(raw) }
func TestApolloRejectsForgedIdentityAndRuntime(t *testing.T) {
	token := strings.Repeat("t", 32)
	base := &proto.RunJobRequest{Name: "backup-backup-a", Image: roverImage(), Prefix: "/app/rover", Command: "handleBackupJob", ArgsBase64: encoded(map[string]any{"backupScheduleId": "backup-a"})}
	for _, tc := range []struct {
		name, key, team, image, command string
		member                          bool
		want                            codes.Code
	}{
		{"valid", token, "team-a", roverImage(), "handleBackupJob", true, codes.OK},
		{"forged", "bad", "team-a", roverImage(), "handleBackupJob", true, codes.Unauthenticated},
		{"foreign", token, "team-b", roverImage(), "handleBackupJob", true, codes.NotFound},
		{"revoked", token, "team-a", roverImage(), "handleBackupJob", false, codes.PermissionDenied},
		{"image", token, "team-a", "alpine", "handleBackupJob", true, codes.InvalidArgument},
		{"command", token, "team-a", roverImage(), "/bin/sh", true, codes.InvalidArgument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := *base
			req.Image = tc.image
			req.Command = tc.command
			ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-service-token", tc.key, "x-team-id", tc.team, "x-user-id", "user-a"))
			called := false
			_, err := Authorization(token, fakeAuthority{tc.member})(ctx, &req, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) { called = true; return nil, nil })
			if status.Code(err) != tc.want {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			if called != (tc.want == codes.OK) {
				t.Fatal("execution boundary mismatch")
			}
		})
	}
}
func TestFlowrChecksEveryConnectionAndIdentity(t *testing.T) {
	spec := map[string]any{"pipeline_id": "p", "teamId": "team-a", "sources": []any{map[string]any{"connection_id": "connection-a"}}, "destination": map[string]any{"connection_id": "rest"}}
	r := &proto.RunJobRequest{Name: "flowr-pipeline-p", Image: flowrImage(), Prefix: "synk", Command: encoded(spec)}
	if err := ValidateJob(context.Background(), fakeAuthority{true}, r, "team-a"); err != nil {
		t.Fatal(err)
	}
	spec["sources"] = []any{map[string]any{"connection_id": "connection-a"}, map[string]any{"connection_id": "foreign"}}
	r.Command = encoded(spec)
	if ValidateJob(context.Background(), fakeAuthority{true}, r, "team-a") == nil {
		t.Fatal("foreign second source accepted")
	}
}
func TestFlowrCapabilitiesAreSeparateAndBounded(t *testing.T) {
	t.Setenv("APOLLO_JOB_SIGNING_KEY", strings.Repeat("k", 32))
	r := runner.JobRequest{Name: "flowr-pipeline-p", AuthorizedUser: "user-a", Command: encoded(map[string]any{"sources": []any{map[string]any{"connection_id": "same"}}, "destination": map[string]any{"connection_id": "same"}})}
	env, err := issueFlowrTokens(r, "team-a")
	if err != nil || len(env) != 2 {
		t.Fatal(err)
	}
	for i, v := range env {
		parts := strings.Split(v.Value, ".")
		raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var claims map[string]any
		json.Unmarshal(raw, &claims)
		scope := claims["connections"].(map[string]any)["same"]
		expected := []string{"read", "write"}[i]
		if scope != expected || claims["team_id"] != "team-a" || claims["exp"].(float64)-claims["iat"].(float64) != 900 {
			t.Fatal("invalid capability")
		}
	}
}
