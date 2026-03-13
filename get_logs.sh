#!/bin/bash
export DEBIAN_FRONTEND=noninteractive
export PYTHONUNBUFFERED=1
sleep 15
gh run list -L 1 -w "AnhurDB-SDK CI/CD"
