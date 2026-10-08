package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	config "github.com/SyneHQ/apollo"
	"github.com/SyneHQ/apollo/connectorjobs"
	"github.com/SyneHQ/apollo/keys"
	"github.com/SyneHQ/apollo/proto"
	"github.com/SyneHQ/apollo/runner"
	"github.com/SyneHQ/apollo/scheduler"
	_secrets "github.com/SyneHQ/apollo/secrets"
	jobsserver "github.com/SyneHQ/apollo/server"
	"google.golang.org/grpc"
)

func main() {
	migrateOnly := flag.Bool("migrate-only", false, "Apply the PostgreSQL scheduler schema and exit before starting providers or jobs")
	flag.Parse()
	operation := run
	if *migrateOnly {
		operation = migrateStore
	}
	if err := operation(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Println("Starting Dramatic Jobs")

	useInfisical := os.Getenv("USE_INFISICAL") == "true"

	secrets, err := keys.NewInfisicalSecrets(useInfisical)

	if err != nil {
		return err
	}

	log.Println("Loading config")

	config, err := config.Load()

	if err != nil {
		return err
	}

	if config.JobsProvider != "hakopod" {
		secrets = _secrets.FilterSecrets(secrets, config.Jobs.Secrets)
	}

	// Choose runner
	var r runner.Runner
	switch config.JobsProvider {
	case "hakopod":
		r, err = runner.NewHakopodRunner(config.Jobs.Hakopod, os.Getenv("APOLLO_HAKOPOD_API_KEY"), os.Getenv("APOLLO_HAKOPOD_CA_PEM"))
		if err != nil {
			return err
		}
	case "cloudrun":
		r = runner.NewBatchRunner(config.GCPProjectID, config.GCPRegion, config.Jobs.Image, secrets)
	default:
		r = runner.NewLocalRunner(config.Jobs.Image, secrets)
	}

	token := os.Getenv("APOLLO_SERVICE_TOKEN")
	if len(token) < 32 {
		return errors.New("APOLLO_SERVICE_TOKEN must contain at least 32 characters")
	}
	if os.Getenv("METADATA_DATABASE_URL") == "" {
		return errors.New("METADATA_DATABASE_URL must be set")
	}
	metadataDB, err := sql.Open("postgres", os.Getenv("METADATA_DATABASE_URL"))
	if err != nil {
		return errors.New("metadata authorization database configuration is invalid")
	}
	defer metadataDB.Close()
	metadataDB.SetMaxOpenConns(10)
	metadataDB.SetMaxIdleConns(2)
	metadataDB.SetConnMaxLifetime(30 * time.Minute)
	startup, cancelStartup := context.WithTimeout(ctx, 20*time.Second)
	defer cancelStartup()
	if err := metadataDB.PingContext(startup); err != nil {
		return errors.New("metadata authorization database is unavailable")
	}
	authority := jobsserver.SQLJobAuthority{DB: metadataDB}
	transport, err := jobsserver.TransportOptions(config.Environment)
	if err != nil {
		return err
	}
	transport = append(transport, grpc.UnaryInterceptor(jobsserver.Authorization(token, authority)), grpc.MaxRecvMsgSize(128*1024))
	grpcServer := grpc.NewServer(transport...)
	js, err := jobsserver.NewJobsServer(r, config)
	if err != nil {
		return err
	}
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := js.Close(shutdown); err != nil {
			log.Print("scheduler shutdown deadline exceeded")
		}
	}()
	js.SetAuthority(authority)
	if err := js.Reload(startup); err != nil {
		return err
	}
	connectorDone := make(chan struct{})
	var supervisor *connectorjobs.Supervisor
	if os.Getenv("APOLLO_CONNECTORS_ENABLED") == "true" {
		if config.JobsProvider != "local" {
			return errors.New("connector dispatch currently requires JOBS_PROVIDER=local")
		}
		supervisor, err = connectorjobs.NewSupervisor(connectorjobs.Store{DB: metadataDB}, r, connectorjobs.Options{
			Image: os.Getenv("APOLLO_CONNECTOR_IMAGE"), SigningKey: os.Getenv("APOLLO_JOB_SIGNING_KEY"),
			BootstrapOrigin: os.Getenv("CONNECTOR_BOOTSTRAP_ORIGIN"), BridgeOrigin: os.Getenv("CONNECTOR_BRIDGE_ORIGIN"),
			ServiceCA: os.Getenv("CONNECTOR_SERVICE_CA_PEM"),
		})
		if err != nil {
			return err
		}
	}
	lis, err := net.Listen("tcp", ":"+config.Port)
	if err != nil {
		return errors.New("cannot bind gRPC listener")
	}
	defer lis.Close()
	js.Start()
	if supervisor != nil {
		go func() { defer close(connectorDone); supervisor.Run(ctx) }()
	} else {
		close(connectorDone)
	}
	proto.RegisterJobsServiceServer(grpcServer, js)
	serveDone := make(chan error, 1)
	go func() { serveDone <- grpcServer.Serve(lis) }()
	log.Printf("Server starting on port %s", config.Port)
	select {
	case <-ctx.Done():
	case err := <-serveDone:
		if err != nil {
			stop()
			grpcServer.Stop()
			return errors.New("gRPC server stopped unexpectedly")
		}
	}
	stop()
	shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	drain := make(chan struct{})
	go func() { grpcServer.GracefulStop(); close(drain) }()
	select {
	case <-drain:
	case <-shutdown.Done():
		grpcServer.Stop()
	}
	select {
	case <-connectorDone:
	case <-shutdown.Done():
		return errors.New("connector shutdown deadline exceeded")
	}
	return nil
}

func migrateStore() error {
	dsn := os.Getenv("APOLLO_MIGRATION_DATABASE_URL")
	if dsn == "" {
		return errors.New("APOLLO_MIGRATION_DATABASE_URL is required with --migrate-only")
	}
	store, err := scheduler.OpenStore("postgres", dsn)
	if err != nil {
		return errors.New("scheduler migration failed; verify database access and inspect the reviewed schema")
	}
	defer store.Close()
	log.Print("Scheduler migrations completed")
	return nil
}
