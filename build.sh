#!/bin/sh

BUILDPATH="build/filetail"

set -e

mkdir -p $BUILDPATH

go build -o $BUILDPATH/filetail .
cp README.md LICENSE example_config.yml $BUILDPATH

cd $BUILDPATH/..
rm -f filetail.tar.gz
tar -czf filetail.tar.gz `basename $BUILDPATH`
rm -r filetail
