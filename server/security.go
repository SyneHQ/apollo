package server

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/SyneHQ/apollo/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"os"
	"regexp"
	"strings"
)

type JobAuthority interface {
	Member(context.Context, string, string, bool) (bool, error)
	Owner(context.Context, string, bool) (string, error)
	Connection(context.Context, string, string) (bool, error)
}
type SQLJobAuthority struct{ DB *sql.DB }

func (s SQLJobAuthority) Member(ctx context.Context, user, team string, admin bool) (bool, error) {
	var ok bool
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM postgoose_user_teams m JOIN "Team" t ON t.id=m."teamId" WHERE m."userId"=$1 AND m."teamId"=$2 AND NOT m.deleted AND NOT t.deleted AND (NOT $3 OR m.role::text IN ('OWNER','ADMIN')))`, user, team, admin).Scan(&ok)
	return ok, err
}
func (s SQLJobAuthority) Connection(ctx context.Context, id, team string) (bool, error) {
	var ok bool
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM postgoose_connections WHERE id=$1 AND "teamId"=$2 AND NOT deleted)`, id, team).Scan(&ok)
	return ok, err
}
func (s SQLJobAuthority) Owner(ctx context.Context, name string, deleted bool) (string, error) {
	var team string
	var err error
	switch {
	case strings.HasPrefix(name, "backup-"):
		err = s.DB.QueryRowContext(ctx, `SELECT c."teamId" FROM postgoose_backup_crons b JOIN postgoose_connections c ON c.id=b."connectionId" JOIN postgoose_storage_destinations d ON d.id=b."destinationId" JOIN "Team" t ON t.id=c."teamId" WHERE b.id=$1 AND d."teamId"=c."teamId" AND ($2 OR (NOT b.deleted AND NOT c.deleted AND NOT d.deleted)) AND NOT t.deleted`, strings.TrimPrefix(name, "backup-"), deleted).Scan(&team)
	case strings.HasPrefix(name, "flowr-pipeline-"):
		err = s.DB.QueryRowContext(ctx, `SELECT d."teamId" FROM diagonals d JOIN "Team" t ON t.id=d."teamId" WHERE d.id=$1 AND d.type::text='FLOWR' AND ($2 OR NOT d.deleted) AND NOT t.deleted`, strings.TrimPrefix(name, "flowr-pipeline-"), deleted).Scan(&team)
	case strings.HasPrefix(name, "diagonal-job-"):
		err = s.DB.QueryRowContext(ctx, `SELECT d."teamId" FROM diagonal_jobs j JOIN diagonals d ON d.id=j."diagonalId" JOIN postgoose_connections c ON c.id=d."sourceId" LEFT JOIN postgoose_connections dst ON dst.id=d."destinationId" JOIN "Team" t ON t.id=d."teamId" WHERE j."jobId"=$1 AND c."teamId"=d."teamId" AND (dst.id IS NULL OR dst."teamId"=d."teamId") AND ($2 OR (NOT d.deleted AND NOT j.deleted AND NOT c.deleted AND (dst.id IS NULL OR NOT dst.deleted))) AND NOT t.deleted`, name, deleted).Scan(&team)
	default:
		return "", errors.New("unknown job")
	}
	return team, err
}
func decodeJob(s string, out any) error {
	if len(s) > 90000 {
		return errors.New("payload too large")
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(s)
	if err != nil || len(raw) > 65536 {
		return errors.New("invalid payload")
	}
	return json.Unmarshal(raw, out)
}

var jobNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,160}$`)

func ValidateJob(ctx context.Context, a JobAuthority, req *proto.RunJobRequest, team string) error {
	if !jobNamePattern.MatchString(req.Name) {
		return errors.New("invalid job name")
	}
	var args struct {
		BackupScheduleID string `json:"backupScheduleId"`
		JobID            string `json:"jobId"`
	}
	switch {
	case strings.HasPrefix(req.Name, "flowr-pipeline-"):
		if req.Image != flowrImage() || req.Prefix != "synk" || req.ArgsBase64 != "" {
			return errors.New("invalid Flowr runtime")
		}
		var spec struct {
			PipelineID string `json:"pipeline_id"`
			TeamID     string `json:"teamId"`
			Sources    []struct {
				ConnectionID string `json:"connection_id"`
			} `json:"sources"`
			Destination struct {
				ConnectionID string `json:"connection_id"`
				Type         string `json:"type"`
			} `json:"destination"`
		}
		if decodeJob(req.Command, &spec) != nil || req.Name != "flowr-pipeline-"+spec.PipelineID || spec.TeamID != team || len(spec.Sources) < 1 || len(spec.Sources) > 10 {
			return errors.New("invalid pipeline identity")
		}
		for _, source := range spec.Sources {
			ok, err := a.Connection(ctx, source.ConnectionID, team)
			if err != nil || !ok {
				return errors.New("source access denied")
			}
		}
		if spec.Destination.ConnectionID != "rest" {
			ok, err := a.Connection(ctx, spec.Destination.ConnectionID, team)
			if err != nil || !ok {
				return errors.New("destination access denied")
			}
		}
	case strings.HasPrefix(req.Name, "backup-"):
		if req.Image != roverImage() || req.Prefix != "/app/rover" || req.Command != "handleBackupJob" || decodeJob(req.ArgsBase64, &args) != nil || req.Name != "backup-"+args.BackupScheduleID {
			return errors.New("invalid backup job")
		}
	case strings.HasPrefix(req.Name, "diagonal-job-"):
		if req.Image != roverImage() || req.Prefix != "/app/rover" || (req.Command != "migrateJob" && req.Command != "handleRestoreJob") || decodeJob(req.ArgsBase64, &args) != nil || args.JobID == "" {
			return errors.New("invalid diagonal job")
		}
		// Bind the actual worker argument to the same authorized job, not merely its display name.
		if sqlAuthority, ok := a.(SQLJobAuthority); ok {
			var matches bool
			err := sqlAuthority.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM diagonal_jobs j JOIN diagonals d ON d.id=j."diagonalId" WHERE j.id=$1 AND j."jobId"=$2 AND (($3='handleRestoreJob' AND d.type::text='RESTORE') OR ($3='migrateJob' AND d.type::text<>'RESTORE')))`, args.JobID, req.Name, req.Command).Scan(&matches)
			if err != nil || !matches {
				return errors.New("job argument access denied")
			}
		}
	default:
		return errors.New("job is not allowlisted")
	}
	return nil
}
func roverImage() string {
	if image := os.Getenv("APOLLO_ROVER_IMAGE"); image != "" {
		return image
	}
	return "ghcr.io/synehq/rover.ts:sudo"
}
func flowrImage() string {
	if image := os.Getenv("APOLLO_FLOWR_IMAGE"); image != "" {
		return image
	}
	return "ghcr.io/synehq/flowr:sudo"
}
func Authorization(token string, a JobAuthority) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		get := func(key string) string {
			values := md.Get(key)
			if len(values) != 1 {
				return ""
			}
			return values[0]
		}
		user, team := get("x-user-id"), get("x-team-id")
		if len(token) < 32 || subtle.ConstantTimeCompare([]byte(token), []byte(get("x-service-token"))) != 1 || user == "" || team == "" {
			return nil, status.Error(codes.Unauthenticated, "unauthorized")
		}
		_, listing := req.(*proto.ListSchedulesRequest)
		ok, err := a.Member(ctx, user, team, !listing)
		if err != nil {
			return nil, status.Error(codes.Unavailable, "authorization unavailable")
		}
		if !ok {
			return nil, status.Error(codes.PermissionDenied, "access denied")
		}
		var name string
		deleted := false
		switch r := req.(type) {
		case *proto.RunJobRequest:
			name = r.Name
		case *proto.DeleteJobRequest:
			name = r.Name
			deleted = true
		case *proto.UpdateScheduleRequest:
			name = r.Name
		case *proto.ListSchedulesRequest:
			return handler(context.WithValue(context.WithValue(ctx, teamContextKey{}, team), userContextKey{}, user), req)
		default:
			return nil, status.Error(codes.PermissionDenied, "method not allowed")
		}
		owner, err := a.Owner(ctx, name, deleted)
		if err != nil || owner != team {
			return nil, status.Error(codes.NotFound, "job not found")
		}
		if run, ok := req.(*proto.RunJobRequest); ok {
			if ValidateJob(ctx, a, run, team) != nil {
				return nil, status.Error(codes.InvalidArgument, "invalid job configuration")
			}
		}
		return handler(context.WithValue(context.WithValue(ctx, teamContextKey{}, team), userContextKey{}, user), req)
	}
}

type teamContextKey struct{}
type userContextKey struct{}
