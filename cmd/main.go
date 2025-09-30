package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	config "github.com/SyneHQ/apollo"
	"github.com/SyneHQ/apollo/keys"
	"github.com/SyneHQ/apollo/proto"
	"github.com/SyneHQ/apollo/runner"
	_secrets "github.com/SyneHQ/apollo/secrets"
	jobsserver "github.com/SyneHQ/apollo/server"
	"google.golang.org/grpc"
)

func main() {

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
	grpcServer := grpc.NewServer()
	js := jobsserver.NewJobsServer(r, config)
	js.Reload(context.Background())
	proto.RegisterJobsServiceServer(grpcServer, js)
	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			panic(err)
		}
	}()

	log.Printf("Server starting on port %s", config.Port)

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		log.Println("Shutting down server...")
		grpcServer.GracefulStop()
		os.Exit(0)
	}()

	// Wait for interrupt signal to gracefully shutdown the server
	<-c
	log.Println("Shutting down server...")
	grpcServer.GracefulStop()
}
