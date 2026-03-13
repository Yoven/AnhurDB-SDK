#!/bin/bash
export DEBIAN_FRONTEND=noninteractive
export PYTHONUNBUFFERED=1

git add .github/workflows/ci.yml
git commit -m "ci: merge release automation directly into primary ci.yml"
git push
