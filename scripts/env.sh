#!/bin/bash
# source scripts/env.sh   — pins the toolchain this project is known to build with.
#
# AGP 8.7.3 needs JDK 17 and Gradle 8.9 (the wrapper handles Gradle). A newer JDK on
# PATH is the single most common cause of a confusing Gradle failure here, so this
# script looks for 17 specifically instead of trusting whatever `java` resolves to.

_jdk17() {
  if [ -n "$JAVA_HOME" ] && "$JAVA_HOME/bin/java" -version 2>&1 | grep -q '"17'; then
    echo "$JAVA_HOME"; return
  fi
  for c in \
    "$(/usr/libexec/java_home -v 17 2>/dev/null)" \
    "$(brew --prefix openjdk@17 2>/dev/null)/libexec/openjdk.jdk/Contents/Home" \
    "/Applications/Android Studio.app/Contents/jbr/Contents/Home"
  do
    [ -x "$c/bin/java" ] && { echo "$c"; return; }
  done
}

JAVA_HOME="$(_jdk17)"
if [ -z "$JAVA_HOME" ]; then
  echo "scripts/env.sh: no JDK 17 found. brew install openjdk@17" >&2
else
  export JAVA_HOME
  export PATH="$JAVA_HOME/bin:$PATH"
fi

export ANDROID_HOME="${ANDROID_HOME:-$HOME/Library/Android/sdk}"
export ANDROID_SDK_ROOT="$ANDROID_HOME"
export PATH="$ANDROID_HOME/platform-tools:$ANDROID_HOME/cmdline-tools/latest/bin:$PATH"

echo "JAVA_HOME=$JAVA_HOME"
echo "ANDROID_HOME=$ANDROID_HOME"
