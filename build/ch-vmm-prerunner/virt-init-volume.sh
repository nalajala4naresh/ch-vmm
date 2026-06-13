#!/bin/sh

set -o errexit
set -o nounset
set -o pipefail

case $1 in
  "cloud-init")
    temp=$(mktemp -d)
    echo "$2" | base64 -d > $temp/meta-data

    if [[ "$3" =~ ^/.* ]]; then
      cp $3 $temp/user-data
    elif [[ -n "$3" ]]; then
      echo "$3" | base64 -d > $temp/user-data
    else
      # Create minimal user-data when none provided
      cat > $temp/user-data << 'EOF'
#cloud-config
# Minimal cloud-init configuration
EOF
    fi

    if [[ "$4" =~ ^/.* ]]; then
      cp $4 $temp/network-config
    elif [[ -n "$4" ]]; then
      echo "$4" | base64 -d > $temp/network-config
    else
      # Create empty network-config file when no network data is provided
      touch $temp/network-config
    fi

    genisoimage -volid cidata -joliet -rock -input-charset utf-8 -output $5 $temp
    ;;
esac
