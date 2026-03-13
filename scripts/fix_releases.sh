#!/bin/bash
export DEBIAN_FRONTEND=noninteractive
export PYTHONUNBUFFERED=1

git add .github/workflows/ci.yml
git commit -m "ci: enforce specific monorepo tag paths for separate SDK deployments"
git push

gh release delete v2.0.0 -y || true
git tag -d v2.0.0 || true
git push origin --delete v2.0.0 || true

gh release create v2/python/v2.0.0 --title "Python SDK v2.0.0" --notes "Initial V2 Release for Python SDK"
gh release create v2/golang/v2.0.0 --title "Golang SDK v2.0.0" --notes "Initial V2 Release for Golang SDK"
