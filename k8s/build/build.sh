#!/bin/bash

# Exit on error
set -e

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

echo -e "${GREEN}🚀 Starting Watchdog Build Process...${NC}"

# Function to check if Docker is running
check_docker() {
    if ! docker info > /dev/null 2>&1; then
        echo -e "${RED}❌ Docker is not running. Please start Docker Desktop first.${NC}"
        exit 1
    fi
}

# Check Docker
check_docker

# Pull Docker image from Docker Hub
echo -e "${GREEN}📥 Pulling Docker image from Docker Hub...${NC}"
docker pull opentransitsoftwarefoundation/watchdog:latest

# Start minikube if not running
if ! minikube status &> /dev/null; then
    echo -e "${GREEN}🚀 Starting minikube...${NC}"
    minikube start --driver=docker --memory=3800 --cpus=2
fi

# Create namespace
echo -e "${GREEN}📝 Creating namespace...${NC}"
kubectl create namespace watchdog-ns --dry-run=client -o yaml | kubectl apply -f -

# Apply manifests
echo -e "${GREEN}📄 Applying manifests...${NC}"
kubectl apply -f ../configmap.yaml -f ../deployment.yaml -f ../service.yaml

# Get service URL
echo -e "${GREEN}🌐 Getting service URL...${NC}"
NODE_PORT=$(kubectl get service watchdog-service -n watchdog-ns -o jsonpath='{.spec.ports[0].nodePort}')
MINIKUBE_IP=$(minikube ip)

echo -e "${GREEN}✨ Setup completed successfully!${NC}"
echo -e "${GREEN}To access the application: http://${MINIKUBE_IP}:${NODE_PORT}${NC}"
echo -e "${YELLOW}To view logs: kubectl logs -f deployment/watchdog -n watchdog-ns${NC}"
echo -e "${YELLOW}To delete everything: kubectl delete namespace watchdog-ns${NC}"

# Print status
echo -e "${GREEN}📊 Current status:${NC}"
kubectl get all -n watchdog-ns 
