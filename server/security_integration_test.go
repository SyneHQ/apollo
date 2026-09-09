package server

import (
	"context"
	"database/sql"
	config "github.com/SyneHQ/apollo"
	"github.com/SyneHQ/apollo/proto"
	"github.com/SyneHQ/apollo/runner"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

type executionProbe struct{ count int }

func (p *executionProbe) RunJob(context.Context, string, runner.JobRequest) (string, error) {
	p.count++
	return "completed", nil
}
func (p *executionProbe) DeleteJob(context.Context, string) error              { return nil }
func (p *executionProbe) UpdateSchedule(context.Context, string, string) error { return nil }
func TestRealMetadataAndGRPCExecution(t *testing.T) {
	dsn := os.Getenv("SECURITY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	for _, q := range []string{`CREATE TEMP TABLE "Team"(id text,deleted boolean)`, `CREATE TEMP TABLE postgoose_user_teams("userId" text,"teamId" text,role text,deleted boolean)`, `CREATE TEMP TABLE postgoose_connections(id text,"teamId" text,deleted boolean)`, `CREATE TEMP TABLE postgoose_storage_destinations(id text,"teamId" text,deleted boolean)`, `CREATE TEMP TABLE postgoose_backup_crons(id text,"connectionId" text,"destinationId" text,deleted boolean)`, `INSERT INTO "Team" VALUES ('team-a',false),('team-b',false)`, `INSERT INTO postgoose_user_teams VALUES ('user-a','team-a','OWNER',false)`, `INSERT INTO postgoose_connections VALUES ('ca','team-a',false),('cb','team-b',false)`, `INSERT INTO postgoose_storage_destinations VALUES ('da','team-a',false),('db','team-b',false)`, `INSERT INTO postgoose_backup_crons VALUES ('good','ca','da',false),('foreign','cb','db',false),('mixed','ca','db',false)`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	authority := SQLJobAuthority{DB: db}
	token := strings.Repeat("t", 32)
	server := grpc.NewServer(grpc.UnaryInterceptor(Authorization(token, authority)))
	probe := &executionProbe{}
	jobs := NewJobsServer(probe, &config.Config{})
	jobs.SetAuthority(authority)
	proto.RegisterJobsServiceServer(server, jobs)
	listener := bufconn.Listen(1024 * 1024)
	go server.Serve(listener)
	defer server.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, "bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := proto.NewJobsServiceClient(conn)
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("x-service-token", token, "x-user-id", "user-a", "x-team-id", "team-a"))
	for _, id := range []string{"good", "foreign", "mixed"} {
		_, err := client.RunJob(ctx, &proto.RunJobRequest{Name: "backup-" + id, Image: roverImage(), Prefix: "/app/rover", Command: "handleBackupJob", ArgsBase64: encoded(map[string]string{"backupScheduleId": id})})
		if (err == nil) != (id == "good") {
			t.Fatalf("job %s: %v", id, err)
		}
	}
	if probe.count != 1 {
		t.Fatalf("executed %d jobs", probe.count)
	}
	if _, err = db.Exec(`UPDATE postgoose_user_teams SET deleted=true`); err != nil {
		t.Fatal(err)
	}
	_, err = client.RunJob(ctx, &proto.RunJobRequest{Name: "backup-good", Image: roverImage(), Prefix: "/app/rover", Command: "handleBackupJob", ArgsBase64: encoded(map[string]string{"backupScheduleId": "good"})})
	if err == nil || probe.count != 1 {
		t.Fatal("revoked owner executed")
	}
}
