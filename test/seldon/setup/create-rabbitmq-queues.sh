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
echo "  Note: Management API may take additional time after pod is ready..."

# Wait for the Management API port to be listening
# If the port is listening, the Management API should be ready
echo "  Waiting for port 15672 to be listening..."
for i in {1..10}; do
  if kubectl -n ${RABBITMQ_NAMESPACE} exec ${RABBITMQ_POD} -c rabbitmq-ha -- \
    sh -c "nc -z localhost 15672 2>/dev/null || ss -tlnp 2>/dev/null | grep -q ':15672 ' || netstat -tlnp 2>/dev/null | grep -q ':15672 '" 2>/dev/null; then
    echo "✓ RabbitMQ management API port is listening"
    break
  fi
  if [ $i -eq 10 ]; then
    echo "ERROR: Port 15672 not listening after 10 attempts"
    echo ">>> Checking pod status..."
    kubectl get pod ${RABBITMQ_POD} -n ${RABBITMQ_NAMESPACE} || true
    exit 1
  fi
  sleep 2
done

# Note: seldon user and permissions are created via definitions file at startup
# Queues are created via Management API (no Queue CRD support in chart)

# Function to create a queue using Management API
# Use kubectl port-forward to access the Management API since curl is not available in the container
create_queue() {
  local queue_name=$1
  echo ">>> Creating queue: ${queue_name}..."
  
  # Use port-forward in background to access Management API
  # Forward port 15672 from pod to localhost
  kubectl -n ${RABBITMQ_NAMESPACE} port-forward ${RABBITMQ_POD} 15672:15672 > /dev/null 2>&1 &
  PORT_FORWARD_PID=$!
  
  # Wait a moment for port-forward to be ready
  sleep 2
  
  # Create queue via Management API
  if curl -s -f -X PUT "http://localhost:15672/api/queues/%2F/${queue_name}" \
    --user "${RABBITMQ_USER}:${RABBITMQ_PASSWORD}" \
    -H "Content-type: application/json" \
    -H "Accept: application/json" \
    -d '{"auto_delete":false,"durable":true,"arguments":{}}' > /dev/null 2>&1; then
    echo "  ✓ Created queue: ${queue_name}"
  else
    echo "  Warning: Failed to create queue ${queue_name} (may already exist)"
  fi
  
  # Kill port-forward
  kill $PORT_FORWARD_PID 2>/dev/null || true
  wait $PORT_FORWARD_PID 2>/dev/null || true
}

# Create test queues
create_queue "iris-model-rabbitmq-input"
create_queue "iris-model-rabbitmq-output"
create_queue "cifar10-rest-input"
create_queue "cifar10-rest-output"

echo ">>> Done creating queues."

