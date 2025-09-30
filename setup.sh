#!/bin/bash

set -e

# Animated ASCII art: reveal line by line
ascii_art=(
"           _.-'''''-._"
"         .'  _     _  '."
"        /   (_)   (_)   \\"
"       |  ,           ,  |"
"       |  \\\`.       .\`/  |"
"        \\  '.\`'\"\"'\`.'  /"
"         '.  \`'---'\`  .'"
"           '-._____.-'"
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
"             .   .   .   .   .   .   .   .   .   .   .   ."
)

for line in "${ascii_art[@]}"; do
    echo "$line"
    sleep 0.07
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

# 5. Set variables for your image
REGISTRY="ghcr.io"
IMAGE_NAME="synehq/apollo-localrun" # <-- CHANGE THIS
SERVICE_NAME="apollo"
TAG="sudo" # or set to a specific tag
PORT_TO_EXPOSE="${PORT_TO_EXPOSE:-6910}"
# 6a. Load environment variables from a file (optional)
# Place key=value pairs in .env (or set ENV_FILE to a different path)
ENV_FILE=${ENV_FILE:-.env}
ENV_CREATE_FLAGS=(--env "ENVIRONMENT=development")
ENV_UPDATE_FLAGS=(--env-add "ENVIRONMENT=development")
if [ -f "$ENV_FILE" ]; then
    echo "Loading environment variables from $ENV_FILE"
    while IFS= read -r line || [ -n "$line" ]; do
        # Skip comments and empty lines
        case "$line" in
            ''|\#*) continue ;;
        esac
        # Preserve full KEY=VALUE (do not export to avoid expanding $VAR references prematurely)
        ENV_CREATE_FLAGS+=("--env" "$line")
        ENV_UPDATE_FLAGS+=("--env-add" "$line")
    done < "$ENV_FILE"
fi

# 6. Login to GHCR (GitHub Container Registry)
echo "Logging in to GHCR..."

read -p "Do you want to log in to GHCR (GitHub Container Registry)? [y/N]: " GHCR_LOGIN_CHOICE
GHCR_LOGIN_CHOICE=${GHCR_LOGIN_CHOICE,,} # to lower case

if [[ "$GHCR_LOGIN_CHOICE" == "y" || "$GHCR_LOGIN_CHOICE" == "yes" ]]; then
    # Use existing GHCR_TOKEN and GHCR_USERNAME if set, otherwise prompt the user
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

# 7. Deploy or update the Docker Swarm service
if docker service ls | grep -q "$SERVICE_NAME"; then
    echo "Updating existing service..."
    if [ ${#ENV_UPDATE_FLAGS[@]} -gt 0 ]; then
        docker service update \
            --image $REGISTRY/$IMAGE_NAME:$TAG \
            --with-registry-auth \
            -p $PORT_TO_EXPOSE:$PORT_TO_EXPOSE \
            "${ENV_UPDATE_FLAGS[@]}" \
            $SERVICE_NAME
    else
        docker service update --image $REGISTRY/$IMAGE_NAME:$TAG --with-registry-auth $SERVICE_NAME
    fi
else
    echo "Creating new service..."
    if [ ${#ENV_CREATE_FLAGS[@]} -gt 0 ]; then
        docker service create \
            --name $SERVICE_NAME \
            --replicas 1 \
            --with-registry-auth \
            -p $PORT_TO_EXPOSE:$PORT_TO_EXPOSE \
            "${ENV_CREATE_FLAGS[@]}" \
            $REGISTRY/$IMAGE_NAME:$TAG
    else
        docker service create \
            --name $SERVICE_NAME \
            --replicas 1 \
            --with-registry-auth \
            -p $PORT_TO_EXPOSE:$PORT_TO_EXPOSE \
            $REGISTRY/$IMAGE_NAME:$TAG
    fi
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

echo "Setup complete. Your service will be automatically updated when a new image is pushed to GHCR."
