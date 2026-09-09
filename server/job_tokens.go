package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/SyneHQ/apollo/runner"
	"github.com/google/uuid"
	"os"
	"time"
)

func signJobToken(key []byte, claims map[string]any) (string, error) {
	if len(key) < 32 {
		return "", errors.New("APOLLO_JOB_SIGNING_KEY is required")
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	raw, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	message := header + "." + base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(message))
	return message + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
func issueFlowrTokens(r runner.JobRequest, team string) ([]runner.EnvVar, error) {
	var spec struct {
		Sources []struct {
			ID string `json:"connection_id"`
		} `json:"sources"`
		Destination struct {
			ID string `json:"connection_id"`
		} `json:"destination"`
	}
	if err := decodeJob(r.Command, &spec); err != nil {
		return nil, err
	}
	read, write := map[string]string{}, map[string]string{}
	for _, source := range spec.Sources {
		read[source.ID] = "read"
	}
	if spec.Destination.ID != "rest" {
		write[spec.Destination.ID] = "write"
	}
	now := time.Now().Unix()
	makeToken := func(connections map[string]string) (string, error) {
		return signJobToken([]byte(os.Getenv("APOLLO_JOB_SIGNING_KEY")), map[string]any{"iss": "syne-apollo", "aud": "syne-db", "sub": r.AuthorizedUser, "team_id": team, "job_id": r.Name, "connections": connections, "iat": now, "exp": now + 900, "jti": uuid.NewString()})
	}
	readToken, err := makeToken(read)
	if err != nil {
		return nil, err
	}
	env := []runner.EnvVar{{Name: "FLOWR_READ_JOB_TOKEN", Value: readToken}}
	if len(write) > 0 {
		writeToken, err := makeToken(write)
		if err != nil {
			return nil, err
		}
		env = append(env, runner.EnvVar{Name: "FLOWR_WRITE_JOB_TOKEN", Value: writeToken})
	}
	return env, nil
}
