import Foundation
import Darwin

public struct LocalIPv4Subnet: Hashable {
    public let interface: String
    public let address: String
    public let netmask: String

    public var hostList: [String] {
        guard let addrInt = ipv4ToUInt32(address),
              let maskInt = ipv4ToUInt32(netmask),
              maskInt != 0 else { return [] }

        let network = addrInt & maskInt
        let broadcast = network | ~maskInt
        let hostCount = broadcast - network
        // Refuse to enumerate networks larger than /16 to avoid 65k probes.
        guard hostCount > 0 && hostCount < 1 << 16 else { return [] }

        var hosts: [String] = []
        hosts.reserveCapacity(Int(hostCount - 1))
        var current = network + 1
        while current < broadcast {
            hosts.append(uint32ToIPv4(current))
            current += 1
        }
        return hosts
    }

    private func ipv4ToUInt32(_ s: String) -> UInt32? {
        let parts = s.split(separator: ".").compactMap { UInt8($0) }
        guard parts.count == 4 else { return nil }
        return parts.reduce(UInt32(0)) { ($0 << 8) | UInt32($1) }
    }

    private func uint32ToIPv4(_ v: UInt32) -> String {
        "\((v >> 24) & 0xFF).\((v >> 16) & 0xFF).\((v >> 8) & 0xFF).\(v & 0xFF)"
    }
}

public enum NetworkInterfaceUtil {
    /// Enumerates active IPv4 interfaces (skipping loopback and link-local)
    /// with their subnet masks. Used by the active sweep to know which hosts
    /// to probe.
    public static func activeIPv4Subnets() -> [LocalIPv4Subnet] {
        var ifaddr: UnsafeMutablePointer<ifaddrs>?
        guard getifaddrs(&ifaddr) == 0, let first = ifaddr else { return [] }
        defer { freeifaddrs(ifaddr) }

        var results: [LocalIPv4Subnet] = []
        var cursor: UnsafeMutablePointer<ifaddrs>? = first
        while let ptr = cursor {
            defer { cursor = ptr.pointee.ifa_next }
            let flags = Int32(ptr.pointee.ifa_flags)
            let isUp = (flags & IFF_UP) == IFF_UP
            let isLoopback = (flags & IFF_LOOPBACK) == IFF_LOOPBACK
            guard isUp, !isLoopback else { continue }

            guard let addrPtr = ptr.pointee.ifa_addr,
                  addrPtr.pointee.sa_family == sa_family_t(AF_INET) else { continue }
            guard let maskPtr = ptr.pointee.ifa_netmask else { continue }

            let name = String(cString: ptr.pointee.ifa_name)
            // Skip utun/awdl/llw/anpi interfaces — VPNs and Apple-internal links.
            if name.hasPrefix("utun") || name.hasPrefix("awdl") ||
                name.hasPrefix("llw") || name.hasPrefix("anpi") ||
                name.hasPrefix("bridge") || name.hasPrefix("ap") {
                continue
            }

            let address = sockaddrToIPv4(addrPtr)
            let netmask = sockaddrToIPv4(maskPtr)
            guard let address, let netmask else { continue }

            // Skip 169.254.* link-local
            if address.hasPrefix("169.254.") { continue }

            results.append(LocalIPv4Subnet(interface: name, address: address, netmask: netmask))
        }
        return results
    }

    private static func sockaddrToIPv4(_ sa: UnsafeMutablePointer<sockaddr>) -> String? {
        var hostBuf = [CChar](repeating: 0, count: Int(NI_MAXHOST))
        let length = socklen_t(MemoryLayout<sockaddr_in>.size)
        let result = getnameinfo(sa, length, &hostBuf, socklen_t(hostBuf.count), nil, 0, NI_NUMERICHOST)
        guard result == 0 else { return nil }
        return String(cString: hostBuf)
    }
}
