#!/bin/bash
export DEBIAN_FRONTEND=noninteractive
export PYTHONUNBUFFERED=1

git add .github/workflows/ci.yml
git commit -m "ci: automate native semantic release and tag auto-increment on push to main"
git push
