#!/bin/bash
export DEBIAN_FRONTEND=noninteractive
export PYTHONUNBUFFERED=1
sleep 40
gh release view v2/python/v2.0.2
