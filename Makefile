run: 
	export $(shell cat .env | xargs) && go run cmd/main.go

dev:
	export $(shell cat .env | xargs) && export INFISICAL_ENV=dev && go run cmd/main.go

test:
	go test -count=1 ./...

kill-local-server:
	kill $(shell lsof -t -i:6910)

units:
	./examples/analytics-container/build.sh --build --test
	sleep 2
	go run examples/analytics_job.go

pre-build:
	# Start the server in the background, wait for it to be ready, then run tests
	make run &
	# Wait for the gRPC server to start (adjust sleep as needed)
	sleep 15
	make test 
	sleep 2
	make kill-local-server
	make units
