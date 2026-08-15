# SOL VPN for Android

This directory contains the first native Android VPN client for SOL.

It removes the Termux + v2rayNG dependency by combining three layers in one app:

1. Android `VpnService` creates the device TUN interface.
2. `hev-socks5-tunnel` converts TUN TCP flows into SOCKS5 and performs mapped DNS locally.
3. The SOL Go core exposes a loopback SOCKS5 listener and sends those TCP streams through the persistent `/mux` WebSocket transport.

The app excludes its own package from the Android VPN route so the SOL WebSocket cannot loop back into the TUN interface.

## DNS

The Android client intentionally uses mapped DNS inside the same process as tun2socks:

- Android is given `198.18.0.2` as the VPN DNS server.
- `hev-socks5-tunnel` maps DNS answers into `240.0.0.0/4`.
- When an application connects to a mapped address, tun2socks restores the original hostname before opening the SOCKS5 stream.
- The SOL server then resolves the hostname normally on the remote side.

This avoids the v2rayNG FakeDNS mismatch where a synthetic IP could reach the SOL server without the local FakeDNS mapping.

## Current beta limitations

- ARM64 Android only (`arm64-v8a`).
- TCP traffic is tunneled. SOL still does not implement SOCKS5 UDP ASSOCIATE, so QUIC and other UDP protocols may fail and applications must fall back to TCP.
- The beta APK is signed with the standard Android debug key for testing. A production release should use a dedicated signing key.
- The token is stored in app-private Android preferences; it is never committed to the repository.

## Build

Requirements:

- Go 1.24+
- JDK 17
- Android SDK platform 34
- Android NDK `27.2.12479018`
- Gradle 8.6

From the repository root:

```sh
sh android/scripts/fetch-hev.sh
sh android/scripts/build-solcore.sh "$ANDROID_SDK_ROOT/ndk/27.2.12479018"
gradle -p android :app:assembleRelease
```

GitHub Actions performs the same build and uploads an installable APK artifact.

## Third-party component

The TUN-to-SOCKS layer uses `hev-socks5-tunnel`, pinned by `android/scripts/fetch-hev.sh`. Its MIT license notice is bundled into the APK under `assets/THIRD_PARTY_NOTICES.txt`.
