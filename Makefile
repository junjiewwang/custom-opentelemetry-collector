
# ===== GENERATED_START =====
# =========================================
# TCS Template Generated Content (2025-12-31T15:27:01+08:00)
# =========================================

# Auto-detect system architecture
ARCH := $(shell uname -m)

# Project name (can be overridden)
PROJECT_NAME ?= custom-otlp-collector

# Map system architecture to Docker architecture
ifeq ($(ARCH),x86_64)
	DOCKER_ARCH = amd64
else ifeq ($(ARCH),aarch64)
	DOCKER_ARCH = arm64
else ifeq ($(ARCH),arm64)
	DOCKER_ARCH = arm64
else
	$(error Unsupported architecture: $(ARCH))
endif

# Generate Dockerfile name based on project and architecture
DOCKERFILE ?= Dockerfile.$(PROJECT_NAME).$(DOCKER_ARCH)

# MINIKUBE flag (default: 0)
MINIKUBE ?= 0

# Docker compose command prefix
ifeq ($(MINIKUBE),1)
	COMPOSE_PREFIX = MINIKUBE=1
else
	COMPOSE_PREFIX =
endif

# Docker compose command with env-file and architecture
COMPOSE_CMD = $(COMPOSE_PREFIX) DOCKERFILE=$(DOCKERFILE) DOCKER_ARCH=$(DOCKER_ARCH) docker compose --env-file .env.make

.PHONY: help clean docker-build docker-up docker-down docker-restart .env.make arch-info \
		check-tools check-kompose check-kubectl \
		k8s-convert k8s-deploy k8s-clean cicd-deploy \
		k8s-status k8s-logs

# Default target
.DEFAULT_GOAL := help

# Show help information
help:
	@echo "========================================="
	@echo "TCS Service Template - Makefile Commands"
	@echo "========================================="
	@echo ""
	@echo "📦 Docker Commands:"
	@echo "  make docker-build          Build Docker image"
	@echo "  make docker-up             Start services with docker compose"
	@echo "  make docker-down           Stop services"
	@echo "  make docker-restart        Rebuild and restart services"
	@echo ""
	@echo "🔧 Tool Check Commands:"
	@echo "  make check-tools           Check all required CI/CD tools"
	@echo "  make check-kompose         Check kompose installation"
	@echo "  make check-kubectl         Check kubectl installation"
	@echo ""
	@echo "☸️  Kubernetes Commands:"
	@echo "  make k8s-convert           Convert compose.yaml to K8s manifests"
	@echo "  make k8s-deploy            Deploy to K8s cluster"
	@echo "  make k8s-full-deploy       Full deployment with ConfigMap (recommended)"
	@echo "  make k8s-status            Check deployment status"
	@echo "  make k8s-logs              View application logs"
	@echo "  make k8s-clean             Clean up K8s resources"
	@echo ""
	@echo "📝 ConfigMap Commands:"
	@echo "  make k8s-configmap         Create ConfigMap from config directory"
	@echo ""
	@echo "🚀 CI/CD Commands:"
	@echo "  make cicd-deploy           Full CI/CD pipeline (build -> convert -> deploy)"
	@echo ""
	@echo "ℹ️  Information Commands:"
	@echo "  make arch-info             Show architecture detection information"
	@echo "  make help                  Show this help message"
	@echo ""
	@echo "🔧 Configuration Variables:"
	@echo "  PROJECT_NAME               Project name (default: custom-otlp-collector)"
	@echo "  K8S_NAMESPACE              Kubernetes namespace (default: default)"
	@echo "  K8S_OUTPUT_DIR             Output directory (default: k8s-manifests)"
	@echo "  K8S_VOLUME_TYPE            Volume type (default: configMap)"
	@echo "                             Options: configMap, persistentVolumeClaim, emptyDir, hostPath"
	@echo "  K8S_CONFIGMAP_NAME         ConfigMap name (default: \${PROJECT_NAME}-config)"
	@echo "  K8S_CONFIG_DIR             Config directory (default: ./.tad/build/custom-otlp-collector/build)"
	@echo "  MINIKUBE                   Minikube mode (default: 0)"
	@echo "  DOCKER_ARCH                Docker architecture (auto-detected)"
	@echo ""
	@echo "📚 Examples:"
	@echo "  make k8s-full-deploy K8S_NAMESPACE=dev"
	@echo "  make k8s-status K8S_NAMESPACE=production"
	@echo "  make k8s-convert K8S_VOLUME_TYPE=persistentVolumeClaim"
	@echo "  make cicd-deploy K8S_VOLUME_TYPE=emptyDir"
	@echo "  make docker-build MINIKUBE=1"
	@echo ""
	@echo "📖 Documentation:"
	@echo "  See docs/K8S_CONFIG_GUIDE.md for detailed guide"
	@echo "  See docs/K8S_QUICK_REFERENCE.md for quick reference"
	@echo "========================================="

# Show architecture information
arch-info:
	@echo "========================================="
	@echo "Architecture Detection Information"
	@echo "========================================="
	@echo "System Architecture: $(ARCH)"
	@echo "Docker Architecture: $(DOCKER_ARCH)"
	@echo "Dockerfile: $(DOCKERFILE)"
	@echo "MINIKUBE Mode: $(MINIKUBE)"
	@echo "========================================="

# ============================================
# CI/CD Tool Detection
# ============================================

# Check if a command exists
check-cmd-%:
	@command -v $* >/dev/null 2>&1 || { \
		echo "ERROR: Required tool '$*' is not installed"; \
		echo "Please install '$*' before proceeding"; \
		exit 1; \
	}
	@echo "✓ $* is available"

# Check all required tools for CI/CD
check-tools: check-cmd-docker check-cmd-kompose check-cmd-kubectl
	@echo "========================================="
	@echo "All required CI/CD tools are available"
	@echo "========================================="

# Check kompose specifically
check-kompose:
	@command -v kompose >/dev/null 2>&1 || { \
		echo "ERROR: kompose is not installed"; \
		echo ""; \
		echo "Installation instructions:"; \
		echo "  macOS:   brew install kompose"; \
		echo "  Linux:   curl -L https://github.com/kubernetes/kompose/releases/download/v1.31.2/kompose-linux-amd64 -o /usr/local/bin/kompose && chmod +x /usr/local/bin/kompose"; \
		echo "  Windows: choco install kubernetes-kompose"; \
		echo ""; \
		echo "Or visit: https://kompose.io/installation/"; \
		exit 1; \
	}
	@echo "✓ kompose is available (version: $(kompose version 2>/dev/null | head -n1 || echo 'unknown'))"

# Check kubectl specifically
check-kubectl:
	@command -v kubectl >/dev/null 2>&1 || { \
		echo "ERROR: kubectl is not installed"; \
		echo ""; \
		echo "Installation instructions:"; \
		echo "  macOS:   brew install kubectl"; \
		echo "  Linux:   curl -LO https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl && chmod +x kubectl && sudo mv kubectl /usr/local/bin/"; \
		echo "  Windows: choco install kubernetes-cli"; \
		echo ""; \
		echo "Or visit: https://kubernetes.io/docs/tasks/tools/"; \
		exit 1; \
	}
	@echo "✓ kubectl is available (version: $(kubectl version --client --short 2>/dev/null || echo 'unknown'))"
	@kubectl cluster-info >/dev/null 2>&1 || { \
		echo "WARNING: kubectl is installed but cannot connect to a cluster"; \
		echo "Please ensure your kubeconfig is properly configured"; \
	}

clean:
	rm -rf bin/custom-otlp-collector
	rm -f .env.make


# Generate .env.make from .tad/devops.yaml
.env.make:
	@echo "# Auto-generated from .tad/devops.yaml" > .env.make
	@grep -A 100 "export_envs:" .tad/devops.yaml | \
		grep -E "^\s+- name:|^\s+value:" | \
		sed 'N;s/.*name: "\(.*\)".* value: "\(.*\)".*/\1=\2/' >> .env.make || true
	@echo "" >> .env.make
	@echo "# Architecture-specific variables" >> .env.make
	@echo "DOCKERFILE=$(DOCKERFILE)" >> .env.make
	@echo "DOCKER_ARCH=$(DOCKER_ARCH)" >> .env.make
	@echo ".env.make generated successfully (Architecture: $(ARCH) -> $(DOCKER_ARCH))"

# Build with docker compose
docker-build: .env.make
	@echo "Building with docker compose (Architecture: $(ARCH) -> $(DOCKER_ARCH), MINIKUBE=$(MINIKUBE))..."
	@echo "Using Dockerfile: $(DOCKERFILE)"
	$(COMPOSE_CMD) build

# Start services with docker compose
docker-up: .env.make
	@echo "Starting services with docker compose (MINIKUBE=$(MINIKUBE))..."
	$(COMPOSE_CMD) up -d

# Stop services
docker-down:
	@echo "Stopping services..."
	docker compose down

# Rebuild and restart
docker-restart: docker-down docker-build docker-up

# ============================================
# Kubernetes Deployment Commands
# ============================================

# K8s output directory
K8S_OUTPUT_DIR ?= k8s-manifests
K8S_NAMESPACE ?= default
K8S_CONFIGMAP_NAME ?= $(PROJECT_NAME)-config
K8S_CONFIG_DIR ?= ./.tad/build/custom-otlp-collector/build

# Volume type for kompose conversion
# Options: configMap (default), persistentVolumeClaim, emptyDir, hostPath
K8S_VOLUME_TYPE ?= configMap

# Convert docker-compose to k8s manifests
k8s-convert: check-kompose .env.make
	@echo "========================================="
	@echo "Converting Docker Compose to K8s manifests"
	@echo "========================================="
	@echo "Output directory: $(K8S_OUTPUT_DIR)"
	@echo "Volume type: $(K8S_VOLUME_TYPE)"
	@mkdir -p $(K8S_OUTPUT_DIR)
	@echo "Running kompose convert..."
	@cd $(K8S_OUTPUT_DIR) && \
		DOCKERFILE=$(DOCKERFILE) DOCKER_ARCH=$(DOCKER_ARCH) \
		kompose convert -f ../compose.yaml --volumes $(K8S_VOLUME_TYPE) || { \
		echo "ERROR: kompose convert failed"; \
		exit 1; \
	}
	@echo "✓ K8s manifests generated successfully in $(K8S_OUTPUT_DIR)/"
	@echo ""
	@echo "Generated files:"
	@ls -lh $(K8S_OUTPUT_DIR)/
	@echo ""
	@if [ "$(K8S_VOLUME_TYPE)" = "configMap" ]; then \
		echo "ℹ️  Note: ConfigMap volumes generated. You may need to:"; \
		echo "   1. Create ConfigMaps with actual config data"; \
		echo "   2. Run 'make k8s-configmap' to generate from files"; \
	elif [ "$(K8S_VOLUME_TYPE)" = "persistentVolumeClaim" ]; then \
		echo "ℹ️  Note: PVC volumes generated. You may need to:"; \
		echo "   1. Create PersistentVolumes in your cluster"; \
		echo "   2. Adjust storage class and size in PVC manifests"; \
	elif [ "$(K8S_VOLUME_TYPE)" = "emptyDir" ]; then \
		echo "ℹ️  Note: emptyDir volumes generated (ephemeral storage)"; \
		echo "   Data will be lost when pod restarts"; \
	fi
	@echo "========================================="

# Deploy to k8s cluster
k8s-deploy: check-kubectl k8s-convert
	@echo "========================================="
	@echo "Deploying to Kubernetes cluster"
	@echo "========================================="
	@if [ ! -d "$(K8S_OUTPUT_DIR)" ] || [ -z "$$(ls -A $(K8S_OUTPUT_DIR) 2>/dev/null)" ]; then \
		echo "ERROR: K8s manifests not found in $(K8S_OUTPUT_DIR)/"; \
		echo "Please run 'make k8s-convert' first"; \
		exit 1; \
	fi
	@echo "Namespace: $(K8S_NAMESPACE)"
	@echo "Applying manifests from $(K8S_OUTPUT_DIR)/..."
	@kubectl apply -f $(K8S_OUTPUT_DIR)/ -n $(K8S_NAMESPACE) || { \
		echo "ERROR: kubectl apply failed"; \
		echo "Please check your cluster connection and manifests"; \
		exit 1; \
	}
	@echo "✓ Deployment successful"
	@echo ""
	@echo "Checking deployment status..."
	@kubectl get all -n $(K8S_NAMESPACE) -l io.kompose.service=$(PROJECT_NAME) 2>/dev/null || \
		kubectl get all -n $(K8S_NAMESPACE) | grep $(PROJECT_NAME) || \
		echo "Note: Use 'kubectl get all -n $(K8S_NAMESPACE)' to check all resources"
	@echo "========================================="

# Clean k8s resources
k8s-clean: check-kubectl
	@echo "========================================="
	@echo "Cleaning Kubernetes resources"
	@echo "========================================="
	@if [ ! -d "$(K8S_OUTPUT_DIR)" ] || [ -z "$$(ls -A $(K8S_OUTPUT_DIR) 2>/dev/null)" ]; then \
		echo "WARNING: K8s manifests not found in $(K8S_OUTPUT_DIR)/"; \
		echo "Skipping kubectl delete"; \
	else \
		echo "Deleting resources from $(K8S_OUTPUT_DIR)/..."; \
		kubectl delete -f $(K8S_OUTPUT_DIR)/ -n $(K8S_NAMESPACE) --ignore-not-found=true || { \
			echo "WARNING: Some resources may not have been deleted"; \
		}; \
		echo "✓ Resources deleted"; \
	fi
	@echo "Removing local manifests directory..."
	@rm -rf $(K8S_OUTPUT_DIR)
	@echo "✓ Local manifests cleaned"
	@echo "========================================="


# Check deployment status
k8s-status: check-kubectl
	@echo "========================================="
	@echo "Kubernetes Deployment Status"
	@echo "========================================="
	@echo "Namespace: $(K8S_NAMESPACE)"
	@echo ""
	@echo "Pods:"
	@kubectl get pods -n $(K8S_NAMESPACE) -l io.kompose.service=$(PROJECT_NAME) 2>/dev/null || \
		kubectl get pods -n $(K8S_NAMESPACE) | grep $(PROJECT_NAME) || \
		echo "No pods found for $(PROJECT_NAME)"
	@echo ""
	@echo "Services:"
	@kubectl get svc -n $(K8S_NAMESPACE) -l io.kompose.service=$(PROJECT_NAME) 2>/dev/null || \
		kubectl get svc -n $(K8S_NAMESPACE) | grep $(PROJECT_NAME) || \
		echo "No services found for $(PROJECT_NAME)"
	@echo ""
	@echo "ConfigMaps:"
	@kubectl get configmap $(K8S_CONFIGMAP_NAME) -n $(K8S_NAMESPACE) 2>/dev/null || \
		echo "ConfigMap $(K8S_CONFIGMAP_NAME) not found"
	@echo "========================================="

# View logs
k8s-logs: check-kubectl
	@echo "========================================="
	@echo "Fetching logs for $(PROJECT_NAME)"
	@echo "========================================="
	@pod=$$(kubectl get pods -n $(K8S_NAMESPACE) -l io.kompose.service=$(PROJECT_NAME) -o jsonpath='{.items[0].metadata.name}' 2>/dev/null); \
	if [ -z "$$pod" ]; then \
		pod=$$(kubectl get pods -n $(K8S_NAMESPACE) -o name | grep $(PROJECT_NAME) | head -n1 | cut -d'/' -f2); \
	fi; \
	if [ -z "$$pod" ]; then \
		echo "ERROR: No pods found for $(PROJECT_NAME)"; \
		exit 1; \
	fi; \
	echo "Pod: $$pod"; \
	echo ""; \
	kubectl logs -f $$pod -n $(K8S_NAMESPACE)


# Full CI/CD pipeline: build -> convert -> deploy (basic)
cicd-deploy: check-tools docker-build k8s-convert k8s-deploy
	@echo "========================================="
	@echo "CI/CD Pipeline Completed Successfully"
	@echo "========================================="
	@echo "Project: $(PROJECT_NAME)"
	@echo "Architecture: $(ARCH) -> $(DOCKER_ARCH)"
	@echo "Namespace: $(K8S_NAMESPACE)"
	@echo "Volume Type: $(K8S_VOLUME_TYPE)"
	@echo ""
	@if [ "$(K8S_VOLUME_TYPE)" = "hostPath" ]; then \
		echo "⚠️  WARNING: Using hostPath volumes (not recommended for production)"; \
		echo "Consider using: K8S_VOLUME_TYPE=configMap or persistentVolumeClaim"; \
	elif [ "$(K8S_VOLUME_TYPE)" = "configMap" ]; then \
		echo "✓ Using ConfigMap volumes (recommended for config files)"; \
		echo "For full ConfigMap support, use: make k8s-full-deploy"; \
	elif [ "$(K8S_VOLUME_TYPE)" = "persistentVolumeClaim" ]; then \
		echo "✓ Using PersistentVolumeClaim (recommended for data persistence)"; \
		echo "Ensure PVs are available in your cluster"; \
	elif [ "$(K8S_VOLUME_TYPE)" = "emptyDir" ]; then \
		echo "ℹ️  Using emptyDir volumes (ephemeral storage)"; \
		echo "Data will be lost when pod restarts"; \
	fi
	@echo ""
	@echo "Next steps:"
	@echo "  - Check status: make k8s-status"
	@echo "  - View logs: make k8s-logs"
	@echo "  - Clean up: make k8s-clean"
	@echo "========================================="

# ============================================
# Custom Targets
# ============================================

.PHONY: test
test: ## Run tests
	go test -v ./...

.PHONY: lint
lint: ## Run linter
	golangci-lint run 
# ===== GENERATED_END =====
