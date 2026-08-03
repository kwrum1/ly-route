#!/bin/sh

# The demo has no physical VPP instance. This reports a harmless locked state
# while exercising the production control API, persistence, and UI paths.
case "$*" in
  "show bond details") exit 0 ;;
  "show ly-route orchestrator") printf 'state locked\n' ;;
  "show interface") printf 'Name                     Idx    State  MTU\n' ;;
  *) exit 0 ;;
esac
