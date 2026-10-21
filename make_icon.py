"""Generate the app icon (rounded gradient square + download arrow)."""
from PIL import Image, ImageDraw

S = 1024
img = Image.new("RGBA", (S, S), (0, 0, 0, 0))

# diagonal gradient
grad = Image.new("RGBA", (S, S))
gd = grad.load()
c1 = (91, 140, 255)
c2 = (139, 108, 255)
for y in range(S):
    for x in range(S):
        t = (x / S * 0.45 + y / S * 0.55)
        gd[x, y] = (
            int(c1[0] + (c2[0] - c1[0]) * t),
            int(c1[1] + (c2[1] - c1[1]) * t),
            int(c1[2] + (c2[2] - c1[2]) * t),
            255,
        )

mask = Image.new("L", (S, S), 0)
ImageDraw.Draw(mask).rounded_rectangle([0, 0, S - 1, S - 1], radius=int(S * 0.225), fill=255)
img.paste(grad, (0, 0), mask)

d = ImageDraw.Draw(img)
white = (255, 255, 255, 255)
w = int(S * 0.075)          # stroke width
cx = S // 2

# arrow shaft
d.rounded_rectangle([cx - w // 2, int(S * 0.22), cx + w // 2, int(S * 0.52)], radius=w // 2, fill=white)
# arrow head
hw = int(S * 0.155)
d.polygon([(cx - hw, int(S * 0.47)), (cx + hw, int(S * 0.47)), (cx, int(S * 0.66))], fill=white)
# baseline
d.rounded_rectangle([int(S * 0.25), int(S * 0.755), int(S * 0.75), int(S * 0.755) + w], radius=w // 2, fill=white)

img.save(r"C:\AI\work\ytdlp-wails\build\appicon.png")
sizes = [(16, 16), (24, 24), (32, 32), (48, 48), (64, 64), (128, 128), (256, 256)]
img.save(r"C:\AI\work\ytdlp-wails\build\windows\icon.ico", sizes=sizes)
img.resize((256, 256), Image.LANCZOS).save(r"C:\AI\work\ytdlp-wails\icon-preview.png")
print("icons written")
