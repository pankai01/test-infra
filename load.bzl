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

load("@bazel_tools//tools/build_defs/repo:http.bzl", "http_archive")
load("@bazel_tools//tools/build_defs/repo:git.bzl", "git_repository", "new_git_repository")

def repositories():
    if not native.existing_rule("io_k8s_repo_infra"):
        http_archive(
            name = "io_k8s_repo_infra",
            strip_prefix = "repo-infra-0.0.2",
            sha256 = "774e160ba1a2a66a736fdc39636dca799a09df015ac5e770a46ec43487ec5708",
            urls = [
                "https://github.com/kubernetes/repo-infra/archive/v0.0.2.tar.gz",
            ],
        )

    http_archive(
        name = "io_bazel_rules_docker",
        sha256 = "dc97fccceacd4c6be14e800b2a00693d5e8d07f69ee187babfd04a80a9f8e250",
        strip_prefix = "rules_docker-0.14.1",
        urls = ["https://github.com/bazelbuild/rules_docker/releases/download/v0.14.1/rules_docker-v0.14.1.tar.gz"],
    )

    http_archive(
        name = "io_bazel_rules_k8s",
        sha256 = "cc75cf0d86312e1327d226e980efd3599704e01099b58b3c2fc4efe5e321fcd9",
        strip_prefix = "rules_k8s-0.3.1",
        urls = ["https://github.com/bazelbuild/rules_k8s/releases/download/v0.3.1/rules_k8s-v0.3.1.tar.gz"],
    )

    # https://github.com/bazelbuild/rules_nodejs
    http_archive(
        name = "build_bazel_rules_nodejs",
        sha256 = "9abd649b74317c9c135f4810636aaa838d5bea4913bfa93a85c2f52a919fdaf3",
        urls = ["https://github.com/bazelbuild/rules_nodejs/releases/download/0.36.0/rules_nodejs-0.36.0.tar.gz"],
    )

    # Python setup
    # pip_import() calls must live in WORKSPACE, otherwise we get a load() after non-load() error
    git_repository(
        name = "rules_python",
        commit = "94677401bc56ed5d756f50b441a6a5c7f735a6d4",
        remote = "https://github.com/bazelbuild/rules_python.git",
        shallow_since = "1573842889 -0500",
    )

    # TODO(fejta): get this to work
    git_repository(
        name = "io_bazel_rules_appengine",
        commit = "fdbce051adecbb369b15260046f4f23684369efc",
        remote = "https://github.com/bazelbuild/rules_appengine.git",
        shallow_since = "1552415147 -0400",
        #tag = "0.0.8+but-this-isn't-new-enough", # Latest at https://github.com/bazelbuild/rules_appengine/releases.
    )

    new_git_repository(
        name = "com_github_operator_framework_community_operators",
        build_file_content = """
exports_files([
    "upstream-community-operators/prometheus/alertmanager.crd.yaml",
    "upstream-community-operators/prometheus/prometheus.crd.yaml",
    "upstream-community-operators/prometheus/prometheusrule.crd.yaml",
    "upstream-community-operators/prometheus/servicemonitor.crd.yaml",
])
""",
        commit = "efda5dc98fd580ab5f1115a50a28825ae4fe6562",
        remote = "https://github.com/operator-framework/community-operators.git",
        shallow_since = "1568320223 +0200",
    )

    http_archive(
        name = "io_bazel_rules_jsonnet",
        sha256 = "68b5bcb0779599065da1056fc8df60d970cffe8e6832caf13819bb4d6e832459",
        strip_prefix = "rules_jsonnet-0.2.0",
        urls = ["https://github.com/bazelbuild/rules_jsonnet/archive/0.2.0.tar.gz"],
    )
