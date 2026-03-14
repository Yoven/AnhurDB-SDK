#!/bin/bash
export DEBIAN_FRONTEND=noninteractive
export PYTHONUNBUFFERED=1

git add .github/workflows/ci.yml
git commit -m "ci: append compiled poetry wheels and tarballs to python automatically generated releases"
git push
