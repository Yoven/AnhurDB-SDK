#!/bin/bash
export DEBIAN_FRONTEND=noninteractive
export PYTHONUNBUFFERED=1
git diff --stat
git diff --cached --stat
grep -n "Record struct" v2/golang/models/record.go
grep -n "NotImplementedError" v2/python/anhurdb/crypto/quantizer.py
