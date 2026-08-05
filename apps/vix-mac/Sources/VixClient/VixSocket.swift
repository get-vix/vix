import Foundation
#if canImport(Darwin)
import Darwin
#elseif canImport(Glibc)
import Glibc
#endif

public enum VixSocketError: Error, CustomStringConvertible {
    case createFailed(String)
    case connectFailed(String)
    case writeFailed(String)
    case readFailed(String)
    case pathTooLong(String)
    case closed

    public var description: String {
        switch self {
        case .createFailed(let s): return "socket create failed: \(s)"
        case .connectFailed(let s): return "connect failed: \(s)"
        case .writeFailed(let s): return "write failed: \(s)"
        case .readFailed(let s): return "read failed: \(s)"
        case .pathTooLong(let p): return "socket path too long: \(p)"
        case .closed: return "socket closed"
        }
    }
}

/// A blocking AF_UNIX stream socket speaking newline-delimited JSON (NDJSON),
/// the vix daemon⇄client wire framing. Directions are independent: a caller may
/// write from one task while `lines()` drains reads on a background queue.
///
/// Two read modes are provided but must not be mixed on the same instance:
/// `readLine()` (synchronous, for one-shot RPCs) and `lines()` (an async stream,
/// for a live thread).
final class VixSocket: @unchecked Sendable {
    private let fd: Int32
    private var syncBuffer = Data()

    init(path: String) throws {
        #if canImport(Glibc)
        let streamType = Int32(SOCK_STREAM.rawValue)
        #else
        let streamType = SOCK_STREAM
        #endif

        let f = socket(AF_UNIX, streamType, 0)
        guard f >= 0 else { throw VixSocketError.createFailed(Self.errnoString()) }

        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)

        let capacity = MemoryLayout.size(ofValue: addr.sun_path)
        let pathBytes = Array(path.utf8)
        guard pathBytes.count < capacity else {
            _ = Darwin_close(f)
            throw VixSocketError.pathTooLong(path)
        }
        withUnsafeMutablePointer(to: &addr.sun_path) { tuplePtr in
            tuplePtr.withMemoryRebound(to: CChar.self, capacity: capacity) { dst in
                for (i, byte) in pathBytes.enumerated() {
                    dst[i] = CChar(bitPattern: byte)
                }
                dst[pathBytes.count] = 0
            }
        }

        let len = socklen_t(MemoryLayout<sockaddr_un>.size)
        let rc = withUnsafePointer(to: &addr) { ptr in
            ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) { saPtr in
                connect(f, saPtr, len)
            }
        }
        guard rc == 0 else {
            let msg = Self.errnoString()
            _ = Darwin_close(f)
            throw VixSocketError.connectFailed(msg)
        }
        self.fd = f
    }

    /// Wrap an already-connected file descriptor. Used by tests to drive framing
    /// over a socketpair without a real daemon.
    init(fd: Int32) {
        self.fd = fd
    }

    func close() {
        _ = Darwin_close(fd)
    }

    // MARK: Writing

    /// Write a single JSON message followed by the '\n' frame delimiter.
    func writeLine(_ data: Data) throws {
        var payload = data
        payload.append(0x0A)
        try payload.withUnsafeBytes { (raw: UnsafeRawBufferPointer) in
            guard let base = raw.baseAddress else { return }
            var total = 0
            let len = raw.count
            while total < len {
                let n = write(fd, base + total, len - total)
                if n < 0 {
                    if errno == EINTR { continue }
                    throw VixSocketError.writeFailed(Self.errnoString())
                }
                total += n
            }
        }
    }

    // MARK: Synchronous read (one-shot RPCs)

    /// Read one newline-delimited message, blocking until a full line arrives.
    func readLine() throws -> Data {
        while true {
            if let nl = syncBuffer.firstIndex(of: 0x0A) {
                let line = syncBuffer.subdata(in: syncBuffer.startIndex..<nl)
                syncBuffer.removeSubrange(syncBuffer.startIndex...nl)
                return line
            }
            var tmp = [UInt8](repeating: 0, count: 65536)
            let n = tmp.withUnsafeMutableBytes { read(fd, $0.baseAddress, 65536) }
            if n < 0 {
                if errno == EINTR { continue }
                throw VixSocketError.readFailed(Self.errnoString())
            }
            if n == 0 { throw VixSocketError.closed }
            syncBuffer.append(contentsOf: tmp[0..<n])
        }
    }

    // MARK: Async read (live thread)

    /// An async stream of newline-delimited messages. Reads run on a dedicated
    /// background queue; the stream finishes on EOF and throws on read error.
    func lines() -> AsyncThrowingStream<Data, Error> {
        let fd = self.fd
        return AsyncThrowingStream { continuation in
            let queue = DispatchQueue(label: "dev.vix.socket.read")
            queue.async {
                var buffer = Data()
                var tmp = [UInt8](repeating: 0, count: 65536)
                while true {
                    let n = tmp.withUnsafeMutableBytes { read(fd, $0.baseAddress, 65536) }
                    if n < 0 {
                        if errno == EINTR { continue }
                        continuation.finish(throwing: VixSocketError.readFailed(Self.errnoString()))
                        return
                    }
                    if n == 0 {
                        continuation.finish()
                        return
                    }
                    buffer.append(contentsOf: tmp[0..<n])
                    while let nl = buffer.firstIndex(of: 0x0A) {
                        let line = buffer.subdata(in: buffer.startIndex..<nl)
                        buffer.removeSubrange(buffer.startIndex...nl)
                        continuation.yield(line)
                    }
                }
            }
        }
    }

    private static func errnoString() -> String {
        String(cString: strerror(errno))
    }
}

// `close` is shadowed by the instance method above; alias the libc symbol.
#if canImport(Darwin)
private let Darwin_close = Darwin.close
#elseif canImport(Glibc)
private let Darwin_close = Glibc.close
#endif
