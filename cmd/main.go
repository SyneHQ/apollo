package main

import (
	"context"
	"database/sql"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	config "github.com/SyneHQ/apollo"
	"github.com/SyneHQ/apollo/connectorjobs"
	"github.com/SyneHQ/apollo/keys"
	"github.com/SyneHQ/apollo/proto"
	"github.com/SyneHQ/apollo/runner"
	_secrets "github.com/SyneHQ/apollo/secrets"
	jobsserver "github.com/SyneHQ/apollo/server"
	"google.golang.org/grpc"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Println("Starting Dramatic Jobs")

	useInfisical := os.Getenv("USE_INFISICAL") == "true"

	secrets, err := keys.NewInfisicalSecrets(useInfisical)

	if err != nil {
		if useInfisical {
			os.Exit(1)
		}
		log.Printf("Error loading infisical secrets: %v", err)
	}

	log.Println("Loading config")

	config, err := config.Load()

	if err != nil {
		panic(err)
	}

	secrets = _secrets.FilterSecrets(secrets, config.Jobs.Secrets)

	// Choose runner
	var r runner.Runner
	switch config.JobsProvider {
	case "cloudrun":
		r = runner.NewBatchRunner(config.GCPProjectID, config.GCPRegion, config.Jobs.Image, secrets)
	default:
		r = runner.NewLocalRunner(config.Jobs.Image, secrets)
	}

	// Start gRPC server
	lis, err := net.Listen("tcp", ":"+config.Port)
	if err != nil {
		panic(err)
	}
	token := os.Getenv("APOLLO_SERVICE_TOKEN")
	if len(token) < 32 {
		log.Fatal("APOLLO_SERVICE_TOKEN must contain at least 32 characters")
	}
	metadataDB, err := sql.Open("postgres", os.Getenv("METADATA_DATABASE_URL"))
	if err != nil {
		log.Fatal("metadata authorization database unavailable")
	}
	defer metadataDB.Close()
	authority := jobsserver.SQLJobAuthority{DB: metadataDB}
	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(jobsserver.Authorization(token, authority)), grpc.MaxRecvMsgSize(128*1024))
	js := jobsserver.NewJobsServer(r, config)
	js.SetAuthority(authority)
	js.Reload(context.Background())
	connectorDone := make(chan struct{})
	if os.Getenv("APOLLO_CONNECTORS_ENABLED") == "true" {
		if config.JobsProvider != "local" {
			log.Fatal("connector dispatch currently requires JOBS_PROVIDER=local")
		}
		supervisor, err := connectorjobs.NewSupervisor(connectorjobs.Store{DB: metadataDB}, r, connectorjobs.Options{
			Image: os.Getenv("APOLLO_CONNECTOR_IMAGE"), SigningKey: os.Getenv("APOLLO_JOB_SIGNING_KEY"),
			BootstrapOrigin: os.Getenv("CONNECTOR_BOOTSTRAP_ORIGIN"), BridgeOrigin: os.Getenv("CONNECTOR_BRIDGE_ORIGIN"),
			ServiceCA: os.Getenv("CONNECTOR_SERVICE_CA_PEM"),
		})
		if err != nil {
			log.Fatal(err)
		}
		go func() { defer close(connectorDone); supervisor.Run(ctx) }()
	} else {
		close(connectorDone)
	}
	proto.RegisterJobsServiceServer(grpcServer, js)
	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			panic(err)
		}
	}()

	log.Printf("Server starting on port %s", config.Port)

	<-ctx.Done()
	log.Println("Shutting down server...")
	grpcServer.GracefulStop()
	<-connectorDone
}
