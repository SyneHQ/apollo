package server

import (
	"context"
	"errors"
	"github.com/SyneHQ/apollo/proto"
	"github.com/SyneHQ/apollo/runner"
	"strings"
	"time"
)

func (s *JobsServer) SetAuthority(a JobAuthority) { s.authority = a }
func (s *JobsServer) runAuthorized(ctx context.Context, prefix string, r runner.JobRequest) (string, error) {
	if s.authority == nil {
		return "", errors.New("authorization unavailable")
	}
	team, err := s.authority.Owner(ctx, r.Name, false)
	if err != nil || team == "" {
		return "", errors.New("job no longer authorized")
	}
	req := &proto.RunJobRequest{Name: r.Name, Image: r.Image, Prefix: r.Prefix, Command: r.Command, ArgsBase64: r.ArgsJSONBase64}
	if err := ValidateJob(ctx, s.authority, req, team); err != nil {
		return "", err
	}
	allowed, err := s.authority.Member(ctx, r.AuthorizedUser, team, true)
	if err != nil || !allowed {
		return "", errors.New("job owner no longer authorized; resave legacy schedules")
	}
	if strings.HasPrefix(r.Name, "flowr-pipeline-") {
		env, err := issueFlowrTokens(r, team)
		if err != nil {
			return "", err
		}
		r.Overrides = &runner.JobOverrides{Env: env}
	}
	timeout := 24 * time.Hour
	if strings.HasPrefix(r.Name, "flowr-pipeline-") {
		timeout = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return s.runner.RunJob(ctx, prefix, r)
}
