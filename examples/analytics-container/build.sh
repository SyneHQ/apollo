#!/bin/bash

# Build script for synehq/analytics container
# This script builds and optionally pushes the analytics container

set -e

# Configuration
IMAGE_NAME="synehq/analytics"
TAG="latest"
FULL_IMAGE_NAME="${IMAGE_NAME}:${TAG}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Function to print colored output
print_status() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

print_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Function to show usage
show_usage() {
    echo "Usage: $0 [OPTIONS]"
    echo ""
    echo "Options:"
    echo "  -b, --build     Build the container image"
    echo "  -p, --push      Push the container image to registry"
    echo "  -t, --test      Test the container locally"
    echo "  -h, --help      Show this help message"
    echo ""
    echo "Examples:"
    echo "  $0 --build                    # Build the image"
    echo "  $0 --build --push            # Build and push the image"
    echo "  $0 --test                    # Test the container locally"
}

# Function to build the container
build_container() {
    print_status "Building container image: ${FULL_IMAGE_NAME}"
    
    # Check if Docker is running
    if ! docker info > /dev/null 2>&1; then
        print_error "Docker is not running. Please start Docker and try again."
        exit 1
    fi
    
    # Build the image
    docker build -t "${FULL_IMAGE_NAME}" .
    
    if [ $? -eq 0 ]; then
        print_success "Container image built successfully: ${FULL_IMAGE_NAME}"
    else
        print_error "Failed to build container image"
        exit 1
    fi
}

# Function to push the container
push_container() {
    print_status "Pushing container image: ${FULL_IMAGE_NAME}"
    
    # Check if image exists locally
    if ! docker image inspect "${FULL_IMAGE_NAME}" > /dev/null 2>&1; then
        print_warning "Image ${FULL_IMAGE_NAME} not found locally. Building first..."
        build_container
    fi
    
    # Push the image
    docker push "${FULL_IMAGE_NAME}"
    
    if [ $? -eq 0 ]; then
        print_success "Container image pushed successfully: ${FULL_IMAGE_NAME}"
    else
        print_error "Failed to push container image"
        exit 1
    fi
}

# Function to test the container
test_container() {
    print_status "Testing container locally..."
    
    # Check if image exists locally
    if ! docker image inspect "${FULL_IMAGE_NAME}" > /dev/null 2>&1; then
        print_warning "Image ${FULL_IMAGE_NAME} not found locally. Building first..."
        build_container
    fi
    
    print_status "Running health check..."
    docker run --rm "${FULL_IMAGE_NAME}" health
    
    print_status "Running version check..."
    docker run --rm "${FULL_IMAGE_NAME}" version
    
    print_status "Testing analytics job with sample parameters..."
    docker run --rm \
        -e DATABASE_PATH="/app/analytics.db" \
        -e API_KEY="test-api-key" \
        -e REDIS_URL="redis://localhost:6379" \
        -e LOG_LEVEL="debug" \
        -e EXECUTION_ID="test-exec-123" \
        -e USER_ID="test-user-456" \
        "${FULL_IMAGE_NAME}" analytics \
        --query-type export \
        --database analytics \
        --output-format json \
        --parallelism 2
    
    print_success "Container test completed successfully!"
}

# Parse command line arguments
BUILD=false
PUSH=false
TEST=false

while [[ $# -gt 0 ]]; do
    case $1 in
        -b|--build)
            BUILD=true
            shift
            ;;
        -p|--push)
            PUSH=true
            shift
            ;;
        -t|--test)
            TEST=true
            shift
            ;;
        -h|--help)
            show_usage
            exit 0
            ;;
        *)
            print_error "Unknown option: $1"
            show_usage
            exit 1
            ;;
    esac
done

# If no options provided, show usage
if [ "$BUILD" = false ] && [ "$PUSH" = false ] && [ "$TEST" = false ]; then
    show_usage
    exit 0
fi

# Execute requested actions
if [ "$BUILD" = true ]; then
    build_container
fi

if [ "$PUSH" = true ]; then
    push_container
fi

if [ "$TEST" = true ]; then
    test_container
fi

print_success "All requested operations completed successfully!"
