#!/bin/bash

set -e

# Animated ASCII art: reveal line by line
ascii_art=(
""
"        APOLLO BY SYNEHQ"
""
"             /\\"
"            /  \\"
"           /----\\"
"          /      \\"
"         /        \\"
"        /----------\\"
"       /            \\"
"      /              \\"
"     /                \\"
"    /------------------\\"
""
"      .      .   .    .    .   .   .   .   .   .   .   .   ."
"   .      .   .   .   .   .   .   .   .   .   .   .   .   ."
"      .   .   .   .   .   .   .   .   .   .   .   .   .   ."
"         .   .   .   .   .   .   .   .   .   .   .   .   ."
)

for line in "${ascii_art[@]}"; do
    echo "$line"
    sleep 0.02
done

# 1. Install Docker if not present
if ! command -v docker &> /dev/null
then
    echo "Docker not found. Installing Docker..."
    # For Ubuntu/Debian
    curl -fsSL https://get.docker.com -o get-docker.sh
    sh get-docker.sh
    rm get-docker.sh
    sudo usermod -aG docker $USER
    echo "Docker installed. Please log out and log back in for group changes to take effect."
else
    echo "Docker is already installed."
fi

# 2. Enable and start Docker
sudo systemctl enable docker
sudo systemctl start docker

# 3. Get the advertising IP for Docker Swarm
# Try to auto-detect the primary IP address
ADVERTISE_ADDR="${SWARM_ADVERTISE_ADDR:-$(hostname -I | awk '{print $1}')}"
echo "Using $ADVERTISE_ADDR as the Docker Swarm advertise address."

# 4. Initialize Docker Swarm if not already initialized
if ! docker info | grep -q "Swarm: active"; then
    echo "Initializing Docker Swarm with advertise address $ADVERTISE_ADDR..."
    docker swarm init --advertise-addr "$ADVERTISE_ADDR"
else
    echo "Docker Swarm already initialized."
fi

echo ""
echo "Which deployment do you want to set up?"
echo "1) Cloud Run (cloudrun image)"
echo "2) Local Docker (localrun image)"
read -p "Enter 1 for Cloud Run or 2 for Localrun [2]: " DEPLOY_CHOICE

ENABLE_DOCKER_SOCK_MOUNT="0"

DEPLOY_CHOICE="${DEPLOY_CHOICE:-2}"

if [[ "$DEPLOY_CHOICE" == "1" ]]; then
    IMAGE_NAME="synehq/apollo-cloudrun"
    SERVICE_NAME="apollo-cloudrun"
    echo "You have selected Cloud Run setup. Using image: $IMAGE_NAME"
else
    IMAGE_NAME="synehq/apollo-localrun"
    SERVICE_NAME="apollo"
    ENABLE_DOCKER_SOCK_MOUNT="1"
    echo "You have selected Localrun setup. Using image: $IMAGE_NAME"
fi

# 5. Set variables for your image
REGISTRY="ghcr.io"
TAG="sudo" # or set to a specific tag

read -p "Which port do you want to expose the service on? [6910]: " PORT_TO_EXPOSE

PORT_TO_EXPOSE="${PORT_TO_EXPOSE:-6910}"

echo "Port to expose: $PORT_TO_EXPOSE"

PORT_TO_EXPOSE="${PORT_TO_EXPOSE:-6910}"

# 6a. Load environment variables from a file (optional)
ENV_FILE=${ENV_FILE:-.env}
ENV_CREATE_FLAGS=(--env "ENVIRONMENT=development" --env "PORT=$PORT_TO_EXPOSE")
ENV_UPDATE_FLAGS=(--env-add "ENVIRONMENT=development" --env "PORT=$PORT_TO_EXPOSE")

if [ -f "$ENV_FILE" ]; then
    echo "Loading environment variables from $ENV_FILE"
    while IFS= read -r line || [ -n "$line" ]; do
        # Skip comments and empty lines
        case "$line" in
            ''|\#*) continue ;;
        esac
        ENV_CREATE_FLAGS+=("--env" "$line")
        ENV_UPDATE_FLAGS+=("--env-add" "$line")
    done < "$ENV_FILE"
fi

# 6. Login to GHCR (GitHub Container Registry)
echo "Logging in to GHCR..."

read -p "Do you want to log in to GHCR (GitHub Container Registry)? [y/N]: " GHCR_LOGIN_CHOICE
GHCR_LOGIN_CHOICE=${GHCR_LOGIN_CHOICE,,} # to lower case

if [[ "$GHCR_LOGIN_CHOICE" == "y" || "$GHCR_LOGIN_CHOICE" == "yes" ]]; then
    if [ -z "$GHCR_TOKEN" ]; then
        echo "Enter your GitHub Personal Access Token (with 'read:packages' scope):"
        read -s GHCR_TOKEN
    fi

    if [ -z "$GHCR_USERNAME" ]; then
        echo "Enter your GitHub Username:"
        read -r GHCR_USERNAME
    fi

    if ! echo "$GHCR_TOKEN" | docker login ghcr.io -u "$GHCR_USERNAME" --password-stdin; then
        echo "GHCR login failed. Please check your token and try again."
        exit 1
    fi
else
    echo "Skipping GHCR login. Make sure you are already logged in if you want to pull private images."
fi

# ask path of jobs config
read -p "Enter the path to the jobs config file: (Optional)" JOBS_CONFIG_PATH
JOBS_CONFIG_PATH="${JOBS_CONFIG_PATH}"
echo "Jobs config path: $JOBS_CONFIG_PATH"

# 7. Deploy or update the Docker Swarm service
# Expose port 6910:6910 using Docker Swarm's --publish mode

# Remove the service if it exists, to ensure port publishing is correct
if docker service ls | grep -q "$SERVICE_NAME"; then
    echo "Removing existing service to ensure port publishing is correct..."
    docker service rm $SERVICE_NAME
    # Wait for the service to be fully removed
    while docker service ls | grep -q "$SERVICE_NAME"; do
        sleep 1
    done
fi

echo "Creating new service with correct port publishing..."

DOCKER_MOUNT_FLAG=()
if [ "$ENABLE_DOCKER_SOCK_MOUNT" = "1" ] || [ "$ENABLE_DOCKER_SOCK_MOUNT" = "true" ]; then
    DOCKER_MOUNT_FLAG+=(--mount type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock)
fi

if [ -f "$JOBS_CONFIG_PATH" ]; then
    DOCKER_MOUNT_FLAG+=(--mount type=bind,src=$JOBS_CONFIG_PATH,dst=/app/jobs.yml)
fi

if [ ${#ENV_CREATE_FLAGS[@]} -gt 0 ]; then
    docker service create \
        --name $SERVICE_NAME \
        --replicas 1 \
        --with-registry-auth \
        --publish mode=host,target=$PORT_TO_EXPOSE,published=$PORT_TO_EXPOSE,protocol=tcp \
        "${DOCKER_MOUNT_FLAG[@]}" \
        "${ENV_CREATE_FLAGS[@]}" \
        $REGISTRY/$IMAGE_NAME:$TAG
else
    docker service create \
        --name $SERVICE_NAME \
        --replicas 1 \
        --with-registry-auth \
        --publish mode=host,target=$PORT_TO_EXPOSE,published=$PORT_TO_EXPOSE,protocol=tcp \
        "${DOCKER_MOUNT_FLAG[@]}" \
        $REGISTRY/$IMAGE_NAME:$TAG
fi

# 8. Set up Watchtower for automatic image updates
WATCHTOWER_SERVICE="watchtower"
if docker service ls | grep -q "$WATCHTOWER_SERVICE"; then
    echo "Watchtower service already running."
else
    echo "Deploying Watchtower for automatic image updates..."
    docker service create \
        --name $WATCHTOWER_SERVICE \
        --restart-condition any \
        --mount type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock \
        -e WATCHTOWER_CLEANUP=true \
        -e WATCHTOWER_POLL_INTERVAL=60 \
        -e WATCHTOWER_TRACE=true \
        -e WATCHTOWER_INCLUDE_STOPPED=true \
        -e WATCHTOWER_ROLLING_RESTART=true \
        containrrr/watchtower \
        $SERVICE_NAME
fi

# 9. Set up firewall for $PORT_TO_EXPOSE port
if ufw status | grep -q "$PORT_TO_EXPOSE/tcp"; then
    echo "Firewall rule for $PORT_TO_EXPOSE/tcp already exists."
else
    echo "Adding firewall rule for $PORT_TO_EXPOSE/tcp..."
    ufw allow $PORT_TO_EXPOSE/tcp
fi

echo "Setup complete. Your service will be automatically updated when a new image is pushed to GHCR."
