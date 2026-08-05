// swift-tools-version:6.0
import PackageDescription

let package = Package(
    name: "vix-mac",
    platforms: [
        .macOS(.v14)
    ],
    products: [
        .library(name: "VixProtocol", targets: ["VixProtocol"]),
        .library(name: "VixClient", targets: ["VixClient"]),
        .library(name: "VixMacCore", targets: ["VixMacCore"]),
        .executable(name: "vix-mac-probe", targets: ["vix-mac-probe"]),
        .executable(name: "vix-mac", targets: ["vix-mac"]),
    ],
    targets: [
        // Generated wire-protocol models + the hand-written JSONValue support
        // type. VixProtocol/Generated.swift is produced by `make mac-models`.
        .target(name: "VixProtocol"),

        // Unix-socket NDJSON transport and the high-level thread client.
        .target(name: "VixClient", dependencies: ["VixProtocol"]),

        // Headless CLI proof: open a thread, stream a turn, answer a confirm.
        .executableTarget(name: "vix-mac-probe", dependencies: ["VixClient"]),

        // UI-independent app core: the transcript model + pure event reducer +
        // the observable ThreadModel. Kept out of the executable so the reducer
        // is unit-testable headlessly.
        .target(name: "VixMacCore", dependencies: ["VixClient"]),

        // The SwiftUI app (macOS). Views only; logic lives in VixMacCore.
        .executableTarget(name: "vix-mac", dependencies: ["VixMacCore"]),

        .testTarget(name: "VixProtocolTests", dependencies: ["VixClient"]),
        .testTarget(name: "VixMacCoreTests", dependencies: ["VixMacCore"]),
    ]
)
