#!/bin/bash
set -e

echo "📦 Installing prerequisites for schedulator..."

# Detect OS
OS="$(uname -s)"
ARCH="$(uname -m)"

echo "Detected OS: $OS, Architecture: $ARCH"

# Install Kind
if ! command -v kind &> /dev/null; then
    echo "Installing Kind..."
    if [ "$OS" = "Darwin" ]; then
        # macOS
        if command -v brew &> /dev/null; then
            brew install kind
        else
            # Manual install
            curl -Lo ./kind https://kind.sigs.k8s.io/dl/v0.20.0/kind-darwin-amd64
            chmod +x ./kind
            sudo mv ./kind /usr/local/bin/kind
        fi
    elif [ "$OS" = "Linux" ]; then
        # Linux
        curl -Lo ./kind https://kind.sigs.k8s.io/dl/v0.20.0/kind-linux-amd64
        chmod +x ./kind
        sudo mv ./kind /usr/local/bin/kind
    fi
    echo "✅ Kind installed"
else
    echo "✅ Kind already installed: $(kind version)"
fi

# Install kubectl
if ! command -v kubectl &> /dev/null; then
    echo "Installing kubectl..."
    if [ "$OS" = "Darwin" ]; then
        if command -v brew &> /dev/null; then
            brew install kubectl
        else
            curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/darwin/amd64/kubectl"
            chmod +x kubectl
            sudo mv kubectl /usr/local/bin/
        fi
    elif [ "$OS" = "Linux" ]; then
        curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl"
        chmod +x kubectl
        sudo mv kubectl /usr/local/bin/
    fi
    echo "✅ kubectl installed"
else
    echo "✅ kubectl already installed: $(kubectl version --client --short 2>/dev/null || kubectl version --client)"
fi

# Check Docker
if ! command -v docker &> /dev/null; then
    echo "❌ Docker not found. Please install Docker Desktop:"
    echo "   https://www.docker.com/products/docker-desktop/"
    exit 1
else
    echo "✅ Docker found: $(docker --version)"
fi

echo ""
echo "✅ All prerequisites installed successfully!"
echo ""
echo "Next steps:"
echo "  1. ./scripts/create-kind-cluster.sh test-cluster"
echo "  2. ./scripts/install-kwok.sh test-cluster"
echo "  3. ./scripts/create-kwok-nodes.sh test-cluster"
