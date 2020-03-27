#!/usr/bin/env bash
# Copyright 2019 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# wrapper.sh handles setting up things before / after the test command $@
#
# usage: wrapper.sh my-test-command [my-test-args]
#
# Things wrapper.sh handles:
# - starting / stopping docker-in-docker
# -- configuring the docker daemon for IPv6
# - confuring bazel caching
# - activating GCP service account credentials
# - ensuring GOPATH/bin is in PATH
#
# After handling these things / before cleanup, my-test-command will be invoked,
# and the exit code of my-test-command will be preserved by wrapper.sh

set -o errexit
set -o pipefail
set -o nounset

echo "wrapper.sh] [INFO] Wrapping Test Command: \`$*\`"
echo "wrapper.sh] [INFO] Running in: ${IMAGE}"
echo "wrapper.sh] [INFO] See: https://github.com/kubernetes/test-infra/blob/master/images/krte/wrapper.sh"
printf '%0.s=' {1..80}; echo
echo "wrapper.sh] [SETUP] Performing pre-test setup ..."

# Check if the job has opted-in to bazel remote caching and if so generate 
# .bazelrc entries pointing to the remote cache
export BAZEL_REMOTE_CACHE_ENABLED=${BAZEL_REMOTE_CACHE_ENABLED:-false}
if [[ "${BAZEL_REMOTE_CACHE_ENABLED}" == "true" ]]; then
  echo "wrapper.sh] [SETUP] Bazel remote cache is enabled, generating .bazelrcs ..."
  /usr/local/bin/create_bazel_cache_rcs.sh
  echo "wrapper.sh] [SETUP] Done setting up .bazelrcs"
fi

# optionally enable ipv6 docker
export DOCKER_IN_DOCKER_IPV6_ENABLED=${DOCKER_IN_DOCKER_IPV6_ENABLED:-false}
if [[ "${DOCKER_IN_DOCKER_IPV6_ENABLED}" == "true" ]]; then
  echo "wrapper.sh] [SETUP] Enabling IPv6 in Docker config ..."
  # configure the daemon with ipv6
  mkdir -p /etc/docker/
  cat <<EOF >/etc/docker/daemon.json
{
  "ipv6": true,
  "fixed-cidr-v6": "fc00:db8:1::/64"
}
EOF
  # enable ipv6
  sysctl net.ipv6.conf.all.disable_ipv6=0
  sysctl net.ipv6.conf.all.forwarding=1
  # enable ipv6 iptables
  modprobe -v ip6table_nat
  echo "wrapper.sh] [SETUP] Done enabling IPv6 in Docker config."
fi

# Check if the job has opted-in to docker-in-docker
export DOCKER_IN_DOCKER_ENABLED=${DOCKER_IN_DOCKER_ENABLED:-false}
if [[ "${DOCKER_IN_DOCKER_ENABLED}" == "true" ]]; then
  echo "wrapper.sh] [SETUP] Docker in Docker enabled, initializing ..."
  # If we have opted in to docker in docker, start the docker daemon,
  service docker start
  # the service can be started but the docker socket not ready, wait for ready
  WAIT_N=0
  while true; do
    # docker ps -q should only work if the daemon is ready
    docker ps -q > /dev/null 2>&1 && break
    if [[ ${WAIT_N} -lt 5 ]]; then
      WAIT_N=$((WAIT_N+1))
      echo "wrapper.sh] [SETUP] Waiting for Docker to be ready, sleeping for ${WAIT_N} seconds ..."
      sleep ${WAIT_N}
    else
      echo "wrapper.sh] [SETUP] Reached maximum attempts, not waiting any longer ..."
      break
    fi
  done
  echo "wrapper.sh] [SETUP] Done setting up Docker in Docker."
fi

# add $GOPATH/bin to $PATH
export GOPATH="${GOPATH:-${HOME}/go}"
export PATH="${GOPATH}/bin:${PATH}"
mkdir -p "${GOPATH}/bin"

# Authenticate gcloud, allow failures
if [[ -n "${GOOGLE_APPLICATION_CREDENTIALS:-}" ]]; then
  echo "wrapper.sh] activating service account from GOOGLE_APPLICATION_CREDENTIALS ..."
  gcloud auth activate-service-account --key-file="${GOOGLE_APPLICATION_CREDENTIALS}" || true
fi

if git rev-parse --is-inside-work-tree >/dev/null; then
  echo "wrapper.sh] [SETUP] Setting SOURCE_DATE_EPOCH for build reproducibility ..."
  # Use a reproducible build date based on the most recent git commit timestamp.
  SOURCE_DATE_EPOCH=$(git log -1 --pretty=%ct)
  export SOURCE_DATE_EPOCH
  echo "wrapper.sh] [SETUP] exported SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH}"
fi

# actually run the user supplied command
printf '%0.s=' {1..80}; echo
echo "wrapper.sh] [TEST] Running Test Command: \`$*\` ..."
set +o errexit
"$@"
EXIT_VALUE=$?
set -o errexit
echo "wrapper.sh] [TEST] Test Command exit code: ${EXIT_VALUE}"

# cleanup
if [[ "${DOCKER_IN_DOCKER_ENABLED}" == "true" ]]; then
  printf '%0.s=' {1..80}; echo
  echo "wrapper.sh] [CLEANUP] Cleaning up after docker in docker ..."
  # NOTE: do not fail on these, prefer preserving test command exit status
  docker ps -aq | xargs -r docker rm -f || true
  service docker stop || true
  echo "wrapper.sh] [CLEANUP] Done cleaning up after docker in docker."
fi

# preserve exit value from user supplied command
printf '%0.s=' {1..80}; echo
echo "wrapper.sh] Exiting ${EXIT_VALUE}"
exit ${EXIT_VALUE}
