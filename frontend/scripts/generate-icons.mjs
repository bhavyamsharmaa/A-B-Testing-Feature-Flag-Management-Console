// Generates the raster icons from public/favicon.svg. Run with `npm run icons`.
// sharp is a devDependency used only here; nothing from it ships in the build.
//
//   public/favicon.ico          16 + 32 px, PNG-in-ICO (every current browser reads it)
//   public/apple-touch-icon.png 180 px, opaque (iOS fills transparency with black)
//   public/icon-192.png         192 px, mark on the dark app tile
//   public/icon-512.png         512 px, mark on the dark app tile
import { readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import sharp from 'sharp'

const root = join(dirname(fileURLToPath(import.meta.url)), '..', 'public')
const svg = readFileSync(join(root, 'favicon.svg'))
const TILE = '#08080b' // the console background

/** The mark alone, rendered crisply at `width` px wide (the SVG is square, so it is `width` tall too). */
const mark = (width) => sharp(svg, { density: Math.ceil((72 * width) / 64) * 2 }).resize(width, width).png().toBuffer()

/** The mark centred on a solid dark tile, with room around it for rounded or masked corners. */
async function tile(size) {
  const markWidth = Math.round(size * 0.74)
  return sharp({ create: { width: size, height: size, channels: 4, background: TILE } })
    .composite([{ input: await mark(markWidth), gravity: 'center' }])
    .removeAlpha() // fully opaque: iOS paints transparent pixels black
    .png()
    .toBuffer()
}

/** Wraps PNG images in an ICO container. */
function ico(images) {
  const header = Buffer.alloc(6)
  header.writeUInt16LE(0, 0) // reserved
  header.writeUInt16LE(1, 2) // type: icon
  header.writeUInt16LE(images.length, 4)
  let offset = 6 + images.length * 16
  const entries = images.map(({ size, data }) => {
    const e = Buffer.alloc(16)
    e.writeUInt8(size >= 256 ? 0 : size, 0) // width
    e.writeUInt8(size >= 256 ? 0 : size, 1) // height
    e.writeUInt8(0, 2) // palette size
    e.writeUInt8(0, 3) // reserved
    e.writeUInt16LE(1, 4) // colour planes
    e.writeUInt16LE(32, 6) // bits per pixel
    e.writeUInt32LE(data.length, 8)
    e.writeUInt32LE(offset, 12)
    offset += data.length
    return e
  })
  return Buffer.concat([header, ...entries, ...images.map((i) => i.data)])
}

const write = (name, data) => {
  writeFileSync(join(root, name), data)
  console.log(`wrote public/${name} (${data.length} bytes)`)
}

const faviconImages = await Promise.all([16, 32].map(async (size) => ({ size, data: await mark(size) })))
write('favicon.ico', ico(faviconImages))
write('apple-touch-icon.png', await tile(180))
write('icon-192.png', await tile(192))
write('icon-512.png', await tile(512))
