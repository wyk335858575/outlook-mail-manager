import { execFileSync } from 'node:child_process'
import { readFileSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = dirname(dirname(fileURLToPath(import.meta.url)))
const versionPath = join(root, 'VERSION')
const versionPattern = /^[1-9]\d*\.[0-9]\.[0-9]$/

function parseVersion(value) {
  const match = value.match(versionPattern)
  if (!match) return null
  const [major, minor, patch] = value.split('.').map(Number)
  return { value, major, minor, patch }
}

function compareVersions(left, right) {
  return left.major - right.major || left.minor - right.minor || left.patch - right.patch
}

function releaseTags() {
  return execFileSync('git', ['tag', '--list', 'v*.*.*'], { cwd: root, encoding: 'utf8' })
    .split(/\r?\n/)
    .filter(Boolean)
    .map((tag) => ({ tag, version: parseVersion(tag.slice(1)) }))
    .filter((item) => item.version !== null)
}

function assertSequentialRelease(version, tags) {
  const candidate = parseVersion(version)
  if (!candidate) throw new Error(`版本 ${version} 不符合 M.N.P 格式；次版本和补丁版本必须为 0 到 9`)
  const previous = tags
    .filter((item) => item.version.value !== version)
    .map((item) => item.version)
    .sort(compareVersions)
    .at(-1)

  if (!previous) {
    if (version !== '1.0.0') throw new Error('首个正式版本必须为 v1.0.0')
    return
  }

  const expected = nextVersion(previous).value
  if (version !== expected) {
    throw new Error(`v${version} 必须紧接 v${previous.value} 发布；下一版本应为 v${expected}`)
  }
}

function readVersion() {
  const value = readFileSync(versionPath, 'utf8').trim()
  if (!versionPattern.test(value)) throw new Error(`VERSION 必须使用 MAJOR.MINOR.PATCH 格式，次版本和补丁版本必须为 0 到 9，当前为 ${value || '<empty>'}`)
  return value
}

function readJSON(path) {
  return JSON.parse(readFileSync(join(root, path), 'utf8'))
}

function writeJSON(path, value) {
  writeFileSync(join(root, path), `${JSON.stringify(value, null, 2)}\n`)
}

function check(version) {
  const packageJSON = readJSON('web/package.json')
  const packageLock = readJSON('web/package-lock.json')
  const env = readFileSync(join(root, '.env.example'), 'utf8')
  const compose = readFileSync(join(root, 'docker-compose.yml'), 'utf8')
  const changelog = readFileSync(join(root, 'CHANGELOG.md'), 'utf8')
  const failures = []

  if (packageJSON.version !== version) failures.push('web/package.json')
  if (packageLock.version !== version || packageLock.packages?.['']?.version !== version) failures.push('web/package-lock.json')
  if (!env.includes(`APP_VERSION=${version}`) || !env.includes(`outlook-mail-manager:${version}`)) failures.push('.env.example')
  if (!compose.includes(`APP_VERSION:-${version}`) || !compose.includes(`outlook-mail-manager:${version}`)) failures.push('docker-compose.yml')
  if (/^\s+build:/m.test(compose)) failures.push('docker-compose.yml（生产编排禁止本地构建）')
  const escapedVersion = version.replaceAll('.', '\\.')
  if (!new RegExp(`^##\\s+${escapedVersion}(?:\\s+-\\s+\\d{4}-\\d{2}-\\d{2})?\\s*$`, 'm').test(changelog)) failures.push('CHANGELOG.md')

  if (failures.length > 0) throw new Error(`以下文件未与 VERSION=${version} 同步：${failures.join(', ')}`)
}

function nextVersion(version) {
  const next = version.patch < 9
    ? `${version.major}.${version.minor}.${version.patch + 1}`
    : version.minor < 9
      ? `${version.major}.${version.minor + 1}.0`
      : `${version.major + 1}.0.0`
  return parseVersion(next)
}

function bump(version) {
  const next = nextVersion(parseVersion(version)).value
  writeFileSync(versionPath, `${next}\n`)

  const packageJSON = readJSON('web/package.json')
  packageJSON.version = next
  writeJSON('web/package.json', packageJSON)

  const packageLock = readJSON('web/package-lock.json')
  packageLock.version = next
  packageLock.packages[''].version = next
  writeJSON('web/package-lock.json', packageLock)

  for (const path of ['.env.example', 'docker-compose.yml']) {
    const fullPath = join(root, path)
    const content = readFileSync(fullPath, 'utf8')
    writeFileSync(fullPath, content.replaceAll(version, next))
  }
  process.stdout.write(`版本已更新为 ${next}。请补充 CHANGELOG.md 后运行 node scripts/version.mjs check。\n`)
}

function releaseCheck(version) {
  check(version)
  const tag = process.env.GITHUB_REF_NAME?.trim()
  if (tag !== `v${version}`) throw new Error(`发布标签必须为 v${version}，当前为 ${tag || '<empty>'}`)
  assertSequentialRelease(version, releaseTags())
}

function prepareCheck(version) {
  check(version)
  const tags = releaseTags()
  assertSequentialRelease(version, tags)
  if (tags.some((item) => item.version.value === version)) {
    const tagCommit = execFileSync('git', ['rev-list', '-n', '1', `v${version}`], { cwd: root, encoding: 'utf8' }).trim()
    const headCommit = execFileSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8' }).trim()
    if (tagCommit !== headCommit) throw new Error(`v${version} 已存在但不指向当前 main 提交`)
  }
}

const command = process.argv[2] ?? 'check'
function run(command) {
  const version = readVersion()
  if (command === 'check') check(version)
  else if (command === 'bump') {
    check(version)
    bump(version)
  }
  else if (command === 'prepare-check') prepareCheck(version)
  else if (command === 'release-check') releaseCheck(version)
  else throw new Error(`未知命令：${command}`)
}

if (process.argv[1] && fileURLToPath(import.meta.url) === resolve(process.argv[1])) run(command)

export { assertSequentialRelease, nextVersion, parseVersion }
