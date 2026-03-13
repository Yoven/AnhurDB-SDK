#!/bin/bash
export DEBIAN_FRONTEND=noninteractive
export PYTHONUNBUFFERED=1
git add .github/workflows/release.yml
git commit -m "ci: add tag-based release automation workflow"
git push
