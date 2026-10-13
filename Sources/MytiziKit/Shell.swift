import Foundation

public struct ShellResult {
    public var status: Int32
    public var stdout: String
    public var stderr: String
    public var timedOut: Bool

    public init(status: Int32, stdout: String, stderr: String, timedOut: Bool) {
        self.status = status
        self.stdout = stdout
        self.stderr = stderr
        self.timedOut = timedOut
    }

    public var succeeded: Bool { status == 0 && !timedOut }
    /// stdout 与 stderr 合并后的文本，便于直接放进诊断报告。
    public var combined: String {
        [stdout, stderr].filter { !$0.isEmpty }.joined(separator: stdout.hasSuffix("\n") || stdout.isEmpty ? "" : "\n")
    }
}

public enum Shell {
    /// 运行命令并收集输出；超时后先 SIGTERM 再 SIGKILL。
    /// 排查命令（lsof、scutil）在异常网络状态下可能卡住，所以所有调用都必须带超时。
    @discardableResult
    public static func run(
        _ executable: String, _ arguments: [String] = [], timeout: TimeInterval = 5,
        environment: [String: String]? = nil, input: Data? = nil
    ) -> ShellResult {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: executable)
        process.arguments = arguments
        if let environment { process.environment = environment }

        let outPipe = Pipe()
        let errPipe = Pipe()
        process.standardOutput = outPipe
        process.standardError = errPipe
        let inPipe = Pipe()
        process.standardInput = inPipe

        // 不使用 waitUntilExit()：它在 GCD 工作线程上靠轮询 RunLoop 实现，每次要多等几十毫秒，
        // 线程池紧张时还可能迟迟得不到调度，表现为"进程早已退出却报告超时"。
        // 改用 terminationHandler 发信号，读管道用专用线程，整个函数不依赖 GCD 线程池。
        let exited = DispatchSemaphore(value: 0)
        process.terminationHandler = { _ in exited.signal() }
        do {
            try process.run()
        } catch {
            return ShellResult(status: 127, stdout: "", stderr: "无法启动 \(executable)：\(error)", timedOut: false)
        }

        var outData = Data()
        var errData = Data()
        let readers = DispatchGroup()
        let lock = NSLock()
        for (pipe, isOut) in [(outPipe, true), (errPipe, false)] {
            readers.enter()
            Thread.detachNewThread {
                let data = pipe.fileHandleForReading.readDataToEndOfFile()
                lock.lock()
                if isOut { outData = data } else { errData = data }
                lock.unlock()
                readers.leave()
            }
        }
        if let input { try? inPipe.fileHandleForWriting.write(contentsOf: input) }
        try? inPipe.fileHandleForWriting.close()

        var timedOut = false
        if exited.wait(timeout: .now() + timeout) == .timedOut {
            timedOut = true
            process.terminate()
            if exited.wait(timeout: .now() + 1) == .timedOut {
                kill(process.processIdentifier, SIGKILL)
                _ = exited.wait(timeout: .now() + 1)
            }
        }
        _ = readers.wait(timeout: .now() + 2)

        lock.lock()
        defer { lock.unlock() }
        return ShellResult(
            status: timedOut ? -1 : process.terminationStatus,
            stdout: String(decoding: outData, as: UTF8.self),
            stderr: String(decoding: errData, as: UTF8.self),
            timedOut: timedOut
        )
    }
}
