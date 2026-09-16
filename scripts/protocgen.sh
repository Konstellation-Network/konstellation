#!/usr/bin/env bash
# Generates Go code for proto/ with buf + gocosmos + grpc-gateway (the same
# toolchain cosmos/evm uses) and moves it into x/. Run via `make proto-gen`.
set -euo pipefail
cd "$(dirname "$0")/.."

cd proto
buf generate --template buf.gen.gogo.yaml
cd ..

# gocosmos writes to proto/<go_package>/…; relocate into the tree.
cp -r proto/github.com/Konstellation-Network/konstellation/x/* x/
rm -rf proto/github.com
