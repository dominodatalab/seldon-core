#!/bin/bash

# Create RabbitMQ queues via Management API
# This script creates queues that were previously managed via Queue CRDs

set -o nounset -o errexit -o pipefail

RABBITMQ_NAMESPACE=rabbitmq

echo ">>> Getting RabbitMQ credentials..."
# Chart creates main secret 'rabbitmq-ha' with default user password
# Default username comes from values (rabbitmqUsername: rabbitmq)
# Default password is in secret 'rabbitmq-ha' with key 'rabbitmq-password'
# For management API, we can use default user (rabbitmq) or seldon user
RABBITMQ_USER="rabbitmq"  # Default from values.yaml
RABBITMQ_PASSWORD=""

# Try to get default user password from chart's main secret
if kubectl get secret rabbitmq-ha -n ${RABBITMQ_NAMESPACE} &>/dev/null; then
  RABBITMQ_PASSWORD=$(kubectl -n ${RABBITMQ_NAMESPACE} get secret rabbitmq-ha -o jsonpath="{.data.rabbitmq-password}" | base64 --decode 2>/dev/null || echo "")
fi

# Fallback to seldon user if default password not found
if [ -z "$RABBITMQ_PASSWORD" ]; then
  RABBITMQ_USER="seldon"
  if kubectl get secret rabbitmq-ha.seldon -n ${RABBITMQ_NAMESPACE} &>/dev/null; then
    RABBITMQ_PASSWORD=$(kubectl -n ${RABBITMQ_NAMESPACE} get secret rabbitmq-ha.seldon -o jsonpath="{.data.rabbitmq-password}" | base64 --decode 2>/dev/null || echo "seldon-password")
  else
    RABBITMQ_PASSWORD="seldon-password"
  fi
fi

if [ -z "$RABBITMQ_USER" ] || [ -z "$RABBITMQ_PASSWORD" ]; then
  echo "ERROR: Could not determine RabbitMQ credentials"
  exit 1
fi

echo ">>> Getting RabbitMQ pod name..."
RABBITMQ_POD=$(kubectl get pods -n ${RABBITMQ_NAMESPACE} -l app.kubernetes.io/name=rabbitmq-ha -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")

if [ -z "$RABBITMQ_POD" ]; then
  echo "ERROR: RabbitMQ pod not found"
  exit 1
fi

echo ">>> Waiting for RabbitMQ management API to be ready..."
for i in {1..30}; do
  if kubectl -n ${RABBITMQ_NAMESPACE} exec ${RABBITMQ_POD} -c rabbitmq -- \
    curl -s -f -u "${RABBITMQ_USER}:${RABBITMQ_PASSWORD}" \
    "http://localhost:15672/api/overview" > /dev/null 2>&1; then
    echo "✓ RabbitMQ management API is ready"
    break
  fi
  if [ $i -eq 30 ]; then
    echo "ERROR: RabbitMQ management API not ready after 30 attempts"
    exit 1
  fi
  sleep 2
done

# Function to create a queue
create_queue() {
  local queue_name=$1
  echo ">>> Creating queue: ${queue_name}..."
  kubectl -n ${RABBITMQ_NAMESPACE} exec ${RABBITMQ_POD} -c rabbitmq -- \
    curl -X PUT "http://localhost:15672/api/queues/%2F/${queue_name}" \
    --user "${RABBITMQ_USER}:${RABBITMQ_PASSWORD}" \
    -H "Content-type: application/json" \
    -H "Accept: application/json" \
    -d '{"auto_delete":false,"durable":true,"arguments":{}}' \
    -f -s > /dev/null || {
      echo "Warning: Failed to create queue ${queue_name} (may already exist)"
    }
}

# Create test queues
create_queue "iris-model-rabbitmq-input"
create_queue "iris-model-rabbitmq-output"
create_queue "cifar10-rest-input"
create_queue "cifar10-rest-output"

echo ">>> Done creating queues."

