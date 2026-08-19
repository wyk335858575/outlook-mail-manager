import { execFileSync } from 'node:child_process'
import { readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = dirname(dirname(fileURLToPath(import.meta.url)))
const versionPath = join(root, 'VERSION')
const versionPattern = /^1\.0\.(0|[1-9]\d*)$/

function readVersion() {
  const value = readFileSync(versionPath, 'utf8').trim()
  if (!versionPattern.test(value)) throw new Error(`VERSION 必须使用 1.0.N 格式，当前为 ${value || '<empty>'}`)
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
  const changelogVersion = changelog.match(/^##\s+([^\s]+)/m)?.[1]
  if (changelogVersion !== version) failures.push('CHANGELOG.md')

  if (failures.length > 0) throw new Error(`以下文件未与 VERSION=${version} 同步：${failures.join(', ')}`)
}

function bump(version) {
  const [major, minor, patch] = version.split('.').map(Number)
  const next = `${major}.${minor}.${patch + 1}`
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

  const patch = Number(version.split('.')[2])
  const tags = execFileSync('git', ['tag', '--list', 'v1.0.*'], { cwd: root, encoding: 'utf8' })
    .split(/\r?\n/)
    .filter(Boolean)
    .map((value) => value.match(/^v1\.0\.(0|[1-9]\d*)$/))
    .filter(Boolean)
    .map((match) => Number(match[1]))
    .filter((value) => value !== patch)

  if (patch === 0 && tags.length > 0) throw new Error('v1.0.0 只能作为首个正式版本发布')
  if (patch > 0 && (tags.length === 0 || Math.max(...tags) !== patch - 1)) {
    throw new Error(`v${version} 必须紧接 v1.0.${patch - 1} 发布`)
  }
  if (tags.some((value) => value > patch)) throw new Error('禁止发布低于现有正式版本的标签')
}

function prepareCheck(version) {
  check(version)
  const patch = Number(version.split('.')[2])
  const tags = execFileSync('git', ['tag', '--list', 'v1.0.*'], { cwd: root, encoding: 'utf8' })
    .split(/\r?\n/)
    .filter(Boolean)
    .map((value) => value.match(/^v1\.0\.(0|[1-9]\d*)$/))
    .filter(Boolean)
    .map((match) => Number(match[1]))

  const previousTags = tags.filter((value) => value !== patch)
  if (patch === 0 && previousTags.length > 0) throw new Error('v1.0.0 只能作为首个正式版本发布')
  if (patch > 0 && (previousTags.length === 0 || Math.max(...previousTags) !== patch - 1)) {
    throw new Error(`v${version} 必须紧接 v1.0.${patch - 1} 发布`)
  }
  if (previousTags.some((value) => value > patch)) throw new Error('禁止发布低于现有正式版本的标签')
  if (tags.includes(patch)) {
    const tagCommit = execFileSync('git', ['rev-list', '-n', '1', `v${version}`], { cwd: root, encoding: 'utf8' }).trim()
    const headCommit = execFileSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8' }).trim()
    if (tagCommit !== headCommit) throw new Error(`v${version} 已存在但不指向当前 main 提交`)
  }
}

const command = process.argv[2] ?? 'check'
const version = readVersion()
if (command === 'check') check(version)
else if (command === 'bump') {
  check(version)
  bump(version)
}
else if (command === 'prepare-check') prepareCheck(version)
else if (command === 'release-check') releaseCheck(version)
else throw new Error(`未知命令：${command}`)
