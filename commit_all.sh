#!/bin/bash
export DEBIAN_FRONTEND=noninteractive
export PYTHONUNBUFFERED=1
git add v2/python/tests/test_crypto.py
git add .github/workflows/ci.yml
git commit -m "ci: split tests and build steps into 4 parallel jobs and fix python test mock"
git push
