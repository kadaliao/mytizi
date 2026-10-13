import XCTest
import MytiziKit
@testable import MytiziCore

final class InstallerScriptTests: XCTestCase {
    func testDaemonPlistIsValidAndRunsHelperForTheOwner() throws {
        let plist = HelperInstaller.daemonPlist(ownerUID: 501)
        let lint = Shell.run("/usr/bin/plutil", ["-lint", "-"], input: Data(plist.utf8))
        XCTAssertTrue(lint.succeeded, lint.combined)

        let data = try XCTUnwrap(plist.data(using: .utf8))
        let object = try XCTUnwrap(PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any])
        XCTAssertEqual(object["Label"] as? String, AppInfo.helperLabel)
        XCTAssertEqual(object["ProgramArguments"] as? [String], [SystemPaths.helperBinary, "--owner-uid", "501"])
        XCTAssertEqual(object["RunAtLoad"] as? Bool, true)
        XCTAssertEqual(object["KeepAlive"] as? Bool, true, "helper 崩溃后由 launchd 拉起，并在启动时恢复网络")
    }

    func testScriptSyntaxIsValid() {
        let script = HelperInstaller.installScript(appPath: "/Applications/My App's.app", ownerUID: 501)
        let install = Shell.run("/bin/sh", ["-n", "-c", script])
        XCTAssertTrue(install.succeeded, "安装脚本有语法错误：status \(install.status) timedOut \(install.timedOut) stderr \(install.stderr)")
        let uninstall = Shell.run("/bin/sh", ["-n", "-c", HelperInstaller.uninstallScript()])
        XCTAssertTrue(uninstall.succeeded, "卸载脚本：status \(uninstall.status) timedOut \(uninstall.timedOut) stderr \(uninstall.stderr)")
    }

    func testShellAndAppleScriptQuoting() {
        XCTAssertEqual(HelperInstaller.shellQuote("a b'c"), "'a b'\\''c'")
        XCTAssertEqual(HelperInstaller.appleScriptQuote("say \"hi\" \\ there"), "\"say \\\"hi\\\" \\\\ there\"")
        // 带引号和空格的路径经过 shell 引用后必须原样还原。
        let tricky = "/Applications/My App's \"x\".app"
        let result = Shell.run("/bin/sh", ["-c", "printf %s \(HelperInstaller.shellQuote(tricky))"])
        XCTAssertEqual(result.stdout, tricky)
    }

    /// 把脚本里的系统路径换到临时目录后真的执行一遍，验证文件布局、权限和写入顺序。
    /// （chown/launchctl 在测试里替换成 true；其余命令原样运行。）
    func testInstallScriptLaysOutFilesCorrectly() throws {
        let sandbox = FileManager.default.temporaryDirectory.appendingPathComponent("install-test-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: sandbox) }
        let app = sandbox.appendingPathComponent("Applications/MyTizi.app")
        let resources = app.appendingPathComponent("Contents/Resources/core")
        let helpers = app.appendingPathComponent("Contents/Helpers")
        try FileManager.default.createDirectory(at: resources, withIntermediateDirectories: true)
        try FileManager.default.createDirectory(at: helpers, withIntermediateDirectories: true)
        // 真实系统一定有 /Library/LaunchDaemons；PrivilegedHelperTools 则可能不存在，由脚本负责创建。
        try FileManager.default.createDirectory(at: sandbox.appendingPathComponent("Library/LaunchDaemons"), withIntermediateDirectories: true)
        for (name, dir) in [("mihomo", resources), ("geoip.metadb", resources), ("geosite.dat", resources), ("components.json", resources), ("mytizi-helper", helpers)] {
            try Data("content-of-\(name)".utf8).write(to: dir.appendingPathComponent(name))
        }

        var script = HelperInstaller.installScript(appPath: app.path, ownerUID: 501)
        script = script
            .replacingOccurrences(of: "/Library/", with: "\(sandbox.path)/Library/")
            .replacingOccurrences(of: "/usr/sbin/chown root:wheel", with: "/usr/bin/true")
            .replacingOccurrences(of: "-o root -g wheel ", with: "")
            .replacingOccurrences(of: "/bin/launchctl", with: "/usr/bin/true")
        let result = Shell.run("/bin/sh", ["-c", script], timeout: 20)
        XCTAssertTrue(result.succeeded, result.combined)

        let support = sandbox.appendingPathComponent("Library/Application Support/MyTizi")
        func mode(_ url: URL) -> Int? { (try? FileManager.default.attributesOfItem(atPath: url.path))?[.posixPermissions] as? Int }
        XCTAssertEqual(mode(support.appendingPathComponent("bin/mihomo")), 0o755)
        XCTAssertEqual(mode(support.appendingPathComponent("core-home/geoip.metadb")), 0o644)
        XCTAssertEqual(mode(support.appendingPathComponent("core-home/geosite.dat")), 0o644)
        XCTAssertEqual(mode(sandbox.appendingPathComponent("Library/PrivilegedHelperTools/\(AppInfo.helperLabel)")), 0o755)
        XCTAssertTrue(FileManager.default.fileExists(atPath: support.appendingPathComponent("core-home/providers").path))
        XCTAssertEqual(try String(contentsOf: support.appendingPathComponent("components.json")), "content-of-components.json")

        // 写出的 plist 必须合法，并且指向（被替换后的）helper 路径
        let plistURL = sandbox.appendingPathComponent("Library/LaunchDaemons/\(AppInfo.helperLabel).plist")
        XCTAssertTrue(Shell.run("/usr/bin/plutil", ["-lint", plistURL.path]).succeeded)
        XCTAssertEqual(mode(plistURL), 0o644)
        XCTAssertTrue(try String(contentsOf: plistURL).contains("--owner-uid"))
        XCTAssertTrue(FileManager.default.fileExists(atPath: sandbox.appendingPathComponent("Library/Logs/MyTizi").path))
    }

    func testManifestIsWrittenAfterAllOtherFiles() {
        let script = HelperInstaller.installScript(appPath: "/Applications/MyTizi.app", ownerUID: 501)
        let manifest = script.range(of: "components.json\" \"$SUP/components.json\"")
        let helper = script.range(of: "\"$HELPER\"\n")
        let plist = script.range(of: "> \"$PLIST\"")
        let bootstrap = script.range(of: "launchctl bootstrap")
        XCTAssertNotNil(manifest); XCTAssertNotNil(helper); XCTAssertNotNil(plist); XCTAssertNotNil(bootstrap)
        XCTAssertGreaterThan(manifest!.lowerBound, helper!.lowerBound)
        XCTAssertGreaterThan(manifest!.lowerBound, plist!.lowerBound)
        XCTAssertGreaterThan(bootstrap!.lowerBound, manifest!.lowerBound, "清单写完才启动 helper")
    }
}
