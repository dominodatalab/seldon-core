#!/bin/bash
set -o nounset -o errexit -o pipefail
set -e

nopush_flag=''
tag_arg=''
while getopts 'nt:' flag; do
  case "${flag}" in
    n) nopush_flag='true' ;;
    t) tag_arg="${OPTARG}" ;;
    *)
      echo "Unexpected option ${flag}"
      exit 1
      ;;
  esac
done
readonly nopush_flag
readonly tag_arg

SELDON_REPO=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

cd "${SELDON_REPO}"

SOURCE_IMAGE_TAG="$(<version.txt)"

TARGET_GIT_REV_TAG="${SOURCE_IMAGE_TAG}-$(git rev-parse --short HEAD)"
TARGET_IMAGE_TAGS=("${TARGET_GIT_REV_TAG}")

BRANCH_NAME="$(git symbolic-ref --short --quiet HEAD)"
if [ -n "${BRANCH_NAME}" ]; then
  TARGET_IMAGE_TAGS+=("${SOURCE_IMAGE_TAG}-${BRANCH_NAME}.latest")
fi

if [ -n "${tag_arg}" ]; then
  TARGET_IMAGE_TAGS+=("${tag_arg}")
fi

echo -e "\n  Building operator...\n"
cd "$SELDON_REPO/operator"
make docker-build-no-test

echo -e "\n  Building executor...\n"
cd "$SELDON_REPO/executor"
make docker-build

cd "${SELDON_REPO}"

if [ "${nopush_flag}" == "" ]; then

  if [ -f ~/.docker/config.json ] && [ "$(cat ~/.docker/config.json | jq '.auths | has("quay.io")')" == "true" ]; then
    echo -e "[Docker is already logged into quay.io, using existing credentials.]"
  elif [ "${QUAY_USER:-missing}" != "missing" ] && [ "${QUAY_PASSWORD:-missing}" != "missing" ]; then
    echo "$QUAY_PASSWORD" | docker login -u "$QUAY_USER" --password-stdin quay.io
  else
    echo "Push to quay.io requires docker login, either run 'docker login quay.io' or set QUAY_USER and QUAY_PASSWORD before running this script."
    exit 1
  fi

  for TARGET_IMAGE_TAG in "${TARGET_IMAGE_TAGS[@]}"
  do

    echo -e "\n  Tagging operator..."
    docker tag "seldonio/seldon-core-operator:${SOURCE_IMAGE_TAG}" "quay.io/domino/seldon-core-operator:${TARGET_IMAGE_TAG}"
    echo -e "  Tagged operator as ${TARGET_IMAGE_TAG}"

    echo -e "\n  Tagging executor..."
    docker tag "seldonio/seldon-core-executor:${SOURCE_IMAGE_TAG}" "quay.io/domino/seldon-core-executor:${TARGET_IMAGE_TAG}"
    echo -e "  Tagged executor as ${TARGET_IMAGE_TAG}"

    operator_target="quay.io/domino/seldon-core-operator:${TARGET_IMAGE_TAG}"
    echo -e "\n  Pushing operator..."
    docker push "${operator_target}"
    echo -e "  *** Pushed operator to ${operator_target} *** "

    executor_target="quay.io/domino/seldon-core-executor:${TARGET_IMAGE_TAG}"
    echo -e "\n  Pushing executor...\n"
    docker push "${executor_target}"
    echo -e "  *** Pushed executor to ${executor_target} *** \n"

  done

  echo "${TARGET_GIT_REV_TAG}" > ~/.seldon-core-image-tag
fi