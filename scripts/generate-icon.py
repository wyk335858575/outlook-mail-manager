from pathlib import Path

from PIL import Image, ImageDraw


def draw_icon(size: int) -> Image.Image:
    scale = 4
    canvas = size * scale
    image = Image.new("RGBA", (canvas, canvas), (0, 0, 0, 0))
    draw = ImageDraw.Draw(image)

    def rect(box, radius, fill):
        draw.rounded_rectangle(tuple(round(value * scale) for value in box), radius=round(radius * scale), fill=fill)

    def line(points, fill, width):
        draw.line([(round(x * scale), round(y * scale)) for x, y in points], fill=fill, width=max(1, round(width * scale)), joint="curve")

    rect((0, 0, 64, 64), 14, "#0f6cbd")
    rect((11, 17, 53, 47), 5, "#ffffff")
    line(((13, 20), (32, 35), (51, 20)), "#17212b", 3)
    line(((14, 44), (50, 44)), "#41b3e8", 3)
    return image.resize((size, size), Image.Resampling.LANCZOS)


if __name__ == "__main__":
    output = Path(__file__).resolve().parents[1] / "web" / "public" / "favicon.ico"
    output.parent.mkdir(parents=True, exist_ok=True)
    draw_icon(64).save(output, format="ICO", sizes=[(16, 16), (32, 32), (48, 48), (64, 64)])
