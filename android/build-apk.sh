#!/bin/bash
set -e

# STUN Max Android APK Build Script
#
# Usage:
#   ./android/build-apk.sh [version]
#
# Prerequisites:
#   - Go 1.21+
#   - Android SDK (auto-detected or $ANDROID_HOME)
#   - Android NDK (auto-detected from SDK)
#   - gogio (auto-installed if missing)

VERSION=${1:-"dev"}
PROJECT_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BUILD_DIR="$PROJECT_ROOT/build"
APK_NAME="stun_max-android-${VERSION}.apk"

echo "=== STUN Max Android Build (${VERSION}) ==="
echo ""

# --- gogio ---
if ! command -v gogio &> /dev/null; then
    echo "[*] Installing gogio..."
    go install gioui.org/cmd/gogio@latest
fi
echo "gogio: $(which gogio)"

# --- ANDROID_HOME ---
if [ -z "$ANDROID_HOME" ]; then
    for candidate in \
        "$HOME/Library/Android/sdk" \
        "$HOME/Android/Sdk" \
        "/usr/local/lib/android/sdk" \
        "/opt/android-sdk"; do
        if [ -d "$candidate" ]; then
            export ANDROID_HOME="$candidate"
            break
        fi
    done
fi
if [ -z "$ANDROID_HOME" ] || [ ! -d "$ANDROID_HOME" ]; then
    echo "ERROR: Android SDK not found"
    echo "  Set ANDROID_HOME or install SDK to ~/Library/Android/sdk"
    exit 1
fi
echo "SDK:   $ANDROID_HOME"

# --- NDK (pick latest) ---
if [ -z "$ANDROID_NDK_HOME" ]; then
    NDK_DIR=$(ls -d "$ANDROID_HOME/ndk/"* 2>/dev/null | sort -V | tail -1)
    if [ -n "$NDK_DIR" ]; then
        export ANDROID_NDK_HOME="$NDK_DIR"
    fi
fi
if [ -z "$ANDROID_NDK_HOME" ] || [ ! -d "$ANDROID_NDK_HOME" ]; then
    echo "ERROR: Android NDK not found in $ANDROID_HOME/ndk/"
    echo "  Install: sdkmanager 'ndk;27.0.12077973'"
    exit 1
fi
echo "NDK:   $ANDROID_NDK_HOME"

# --- Build ---
mkdir -p "$BUILD_DIR"

# gogio requires version format: major.minor.patch.versioncode
# Convert "v1.0.0" -> "1.0.0.1", "dev" -> skip version flag
GOGIO_ARGS="-target android -appid com.stunmax.app -minsdk 24 -targetsdk 29"
if [ "$VERSION" != "dev" ]; then
    # Strip leading 'v', append .1 as versioncode if missing 4th part
    SEMVER="${VERSION#v}"
    PARTS=$(echo "$SEMVER" | tr '.' '\n' | wc -l | tr -d ' ')
    if [ "$PARTS" -lt 4 ]; then
        SEMVER="${SEMVER}.1"
    fi
    GOGIO_ARGS="$GOGIO_ARGS -version $SEMVER"
fi

echo ""
echo "[*] Building base APK via gogio..."
GOGIO_APK="$BUILD_DIR/${APK_NAME%.apk}-base.apk"
gogio $GOGIO_ARGS \
    -o "$GOGIO_APK" \
    "$PROJECT_ROOT/client/"

echo ""
echo "[*] Post-processing: adding VpnService classes and manifest entries..."

# --- Locate build-tools ---
BUILD_TOOLS_DIR=$(ls -d "$ANDROID_HOME/build-tools/"* 2>/dev/null | sort -V | tail -1)
if [ -z "$BUILD_TOOLS_DIR" ]; then
    echo "WARNING: Android build-tools not found, skipping post-processing"
    cp "$GOGIO_APK" "$BUILD_DIR/$APK_NAME"
else
    AAPT2="$BUILD_TOOLS_DIR/aapt2"
    D8="$BUILD_TOOLS_DIR/d8"
    ZIPALIGN="$BUILD_TOOLS_DIR/zipalign"
    APKSIGNER="$BUILD_TOOLS_DIR/apksigner"

    JAVA_SRC_DIR="$PROJECT_ROOT/android/app/src/main/java"
    WORK_DIR="$BUILD_DIR/apk-work"
    rm -rf "$WORK_DIR"
    mkdir -p "$WORK_DIR/classes" "$WORK_DIR/merged"

    # Find android.jar for compilation
    ANDROID_JAR=""
    for lvl in 34 33 32 31 30 29 28; do
        candidate="$ANDROID_HOME/platforms/android-${lvl}/android.jar"
        if [ -f "$candidate" ]; then
            ANDROID_JAR="$candidate"
            break
        fi
    done

    if [ -z "$ANDROID_JAR" ]; then
        echo "WARNING: android.jar not found, skipping Java class injection"
        cp "$GOGIO_APK" "$BUILD_DIR/$APK_NAME"
    else
        echo "  android.jar: $ANDROID_JAR"

        # Step 1: Compile Java sources
        echo "  Compiling Java classes..."
        JAVA_FILES=$(find "$JAVA_SRC_DIR" -name "*.java" 2>/dev/null)
        if [ -n "$JAVA_FILES" ]; then
            javac -source 8 -target 8 \
                -classpath "$ANDROID_JAR" \
                -d "$WORK_DIR/classes" \
                $JAVA_FILES 2>/dev/null || {
                echo "WARNING: javac failed, skipping class injection"
                cp "$GOGIO_APK" "$BUILD_DIR/$APK_NAME"
                JAVA_FILES=""
            }
        fi

        if [ -n "$JAVA_FILES" ]; then
            # Step 2: Convert to DEX using d8
            echo "  Converting to DEX..."
            CLASS_FILES=$(find "$WORK_DIR/classes" -name "*.class")
            "$D8" --output "$WORK_DIR" \
                --min-api 24 \
                $CLASS_FILES 2>/dev/null || {
                echo "WARNING: d8 failed, skipping class injection"
                cp "$GOGIO_APK" "$BUILD_DIR/$APK_NAME"
                CLASS_FILES=""
            }

            if [ -n "$CLASS_FILES" ]; then
                # Step 3: Unpack the base APK
                echo "  Unpacking base APK..."
                cp "$GOGIO_APK" "$WORK_DIR/merged/app.apk"
                cd "$WORK_DIR/merged"
                unzip -q app.apk -d unpacked
                rm app.apk

                # Step 4: Merge our classes.dex
                # gogio produces classes.dex; our new one goes as classes2.dex
                if [ -f "$WORK_DIR/classes.dex" ]; then
                    # Find next available dex slot
                    DEX_IDX=2
                    while [ -f "unpacked/classes${DEX_IDX}.dex" ]; do
                        DEX_IDX=$((DEX_IDX + 1))
                    done
                    cp "$WORK_DIR/classes.dex" "unpacked/classes${DEX_IDX}.dex"
                    echo "  Injected classes${DEX_IDX}.dex"
                fi

                # Step 5: Replace manifest + inject app icon via aapt2.
                # We compile icon resources with aapt2, then link them together
                # with our manifest so resources.arsc maps @mipmap/ic_launcher.
                echo "  Patching manifest + app icon..."
                MANIFEST_SRC="$PROJECT_ROOT/android/AndroidManifest.xml"
                if [ -f "$MANIFEST_SRC" ] && [ -n "$AAPT2" ] && [ -f "$AAPT2" ]; then
                    RES_WORK="$WORK_DIR/res-work"
                    rm -rf "$RES_WORK"
                    mkdir -p "$RES_WORK/compiled" "$RES_WORK/res"

                    # 5a: Prepare icon PNGs at each density
                    LOGO_SRC="$PROJECT_ROOT/img/logo.png"
                    ICON_COMPILED_ARGS=""
                    if [ -f "$LOGO_SRC" ]; then
                        for density in mdpi:48 hdpi:72 xhdpi:96 xxhdpi:144 xxxhdpi:192; do
                            dname="${density%%:*}"
                            dsize="${density##*:}"
                            mkdir -p "$RES_WORK/res/mipmap-${dname}"
                            if command -v sips &>/dev/null; then
                                sips -z "$dsize" "$dsize" "$LOGO_SRC" \
                                    --out "$RES_WORK/res/mipmap-${dname}/ic_launcher.png" 2>/dev/null
                            else
                                cp "$LOGO_SRC" "$RES_WORK/res/mipmap-${dname}/ic_launcher.png"
                            fi
                        done

                        # 5b: Compile resources with aapt2
                        "$AAPT2" compile --dir "$RES_WORK/res" -o "$RES_WORK/compiled/" 2>/dev/null
                        ICON_COMPILED_ARGS=$(find "$RES_WORK/compiled" -name "*.flat" 2>/dev/null | tr '\n' ' ')
                    fi

                    # 5c: Link manifest + compiled resources into a temporary APK
                    "$AAPT2" link \
                        --manifest "$MANIFEST_SRC" \
                        -I "$ANDROID_JAR" \
                        --min-sdk-version 24 \
                        --target-sdk-version 29 \
                        --version-code 1 \
                        --version-name "${VERSION}" \
                        -o "$RES_WORK/patched.apk" \
                        --no-auto-version \
                        $ICON_COMPILED_ARGS 2>/dev/null

                    if [ -f "$RES_WORK/patched.apk" ]; then
                        # Extract binary manifest + resources.arsc + res/ from the patched APK
                        unzip -q -o "$RES_WORK/patched.apk" AndroidManifest.xml resources.arsc "res/*" \
                            -d "$RES_WORK/extract" 2>/dev/null
                        if [ -f "$RES_WORK/extract/AndroidManifest.xml" ]; then
                            cp "$RES_WORK/extract/AndroidManifest.xml" "unpacked/AndroidManifest.xml"
                            echo "  Manifest patched: VpnService + permissions + icon"
                        fi
                        if [ -f "$RES_WORK/extract/resources.arsc" ]; then
                            cp "$RES_WORK/extract/resources.arsc" "unpacked/resources.arsc"
                            echo "  resources.arsc replaced (icon registered)"
                        fi
                        if [ -d "$RES_WORK/extract/res" ]; then
                            cp -r "$RES_WORK/extract/res/"* "unpacked/res/" 2>/dev/null
                            echo "  Icon resources injected"
                        fi
                    else
                        echo "  WARNING: aapt2 link failed, keeping original manifest"
                    fi
                    rm -rf "$RES_WORK"
                else
                    echo "  WARNING: Manifest source or aapt2 not found, keeping original"
                fi

                # Step 6: Repack APK (unsigned, unaligned)
                echo "  Repacking APK..."
                cd unpacked
                UNSIGNED_APK="$WORK_DIR/unsigned.apk"
                zip -q -r "$UNSIGNED_APK" . -x "META-INF/*"
                cd "$PROJECT_ROOT"

                # Step 7: Zipalign
                echo "  Aligning APK..."
                ALIGNED_APK="$WORK_DIR/aligned.apk"
                "$ZIPALIGN" -f 4 "$UNSIGNED_APK" "$ALIGNED_APK"

                # Step 8: Sign with debug keystore
                echo "  Signing APK..."
                DEBUG_KEYSTORE="$HOME/.android/debug.keystore"
                if [ ! -f "$DEBUG_KEYSTORE" ]; then
                    echo "  Creating debug keystore..."
                    keytool -genkey -v \
                        -keystore "$DEBUG_KEYSTORE" \
                        -storepass android -keypass android \
                        -alias androiddebugkey \
                        -keyalg RSA -keysize 2048 -validity 10000 \
                        -dname "CN=Android Debug,O=Android,C=US" 2>/dev/null
                fi
                "$APKSIGNER" sign \
                    --ks "$DEBUG_KEYSTORE" \
                    --ks-pass pass:android \
                    --ks-key-alias androiddebugkey \
                    --key-pass pass:android \
                    --out "$BUILD_DIR/$APK_NAME" \
                    "$ALIGNED_APK"

                echo "  APK post-processing complete"
            fi
        fi
    fi
fi

# Cleanup
rm -f "$GOGIO_APK"
rm -rf "$BUILD_DIR/apk-work"

echo ""
echo "=== Done ==="
ls -lh "$BUILD_DIR/$APK_NAME"
echo ""
echo "Install:  adb install $BUILD_DIR/$APK_NAME"
