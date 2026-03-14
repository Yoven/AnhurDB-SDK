#!/bin/bash
export DEBIAN_FRONTEND=noninteractive
export PYTHONUNBUFFERED=1

git add .github/workflows/ci.yml
git commit -m "ci: add write permissions to github token to allow workflow attached wheel deployments"
git push
