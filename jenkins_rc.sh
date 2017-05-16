#!/bin/bash

. /home/jenkins/.gvm/scripts/gvm

gvm use go1.7.4
export GOPATH=$WORKSPACE/gopath
export PATH=$WORKSPACE/gopath/bin:$PATH