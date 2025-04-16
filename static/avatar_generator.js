// Maybe I should make a go implementation with this library:
// https://github.com/tdewolff/canvas

/**
 * Generates a random avatar on the given canvas.
 *
 * @param {HTMLCanvasElement} canvas
 */
function drawAvatar(canvas) {
    console.log(canvas);
    const ctx = canvas.getContext("2d");
    ctx.reset();

    const hue = Math.round(Math.random() * 360);
    const lightness = Math.round(Math.random() * 30) + 50;
    const saturation = Math.round(Math.random() * 50) + 50;
    const bgColor = `hsl(${hue}, ${saturation}%, ${lightness}%)`;

    // const mouthHue = (hue + 180) % 360;
    // const mouthHue = (hue + 120 + Math.round(Math.random() * 60)) % 360;
    // const mouthLightness = 100 - lightness;
    // const mouthColor = `hsl(${mouthHue}, ${saturation}%, ${mouthLightness}%)`;

    const w = canvas.width;
    const h = canvas.height;
    const paddingX = 0.05 * w;
    const paddingY = 0.05 * h;

    // all relative to canvas width/height (in [0,1])
    const eyeY = Math.random() * 0.75;
    const eyeSeparation = 0.1 + Math.random() * 0.65;
    const eyeX = (Math.random() * (1.0 - eyeSeparation)) / 2;
    const eyeR = 5 + Math.random() * 15;
    // The 0.05 is some padding to make sure the mouth and eyes aren't in the same line
    const mouthY = eyeY + 0.05 + Math.random() * (0.9 - eyeY);
    // Maybe keep it within [0.1, 0.9]?
    const mouthX = Math.random();
    const mouthR = 5 + Math.random() * 45;

    function x(xCoord01) {
        return paddingX + (w - 2 * paddingX) * xCoord01;
    }
    function y(yCoord01) {
        return paddingY + (h - 2 * paddingY) * yCoord01;
    }

    ctx.fillStyle = bgColor;
    ctx.fillRect(0, 0, w, h);
    // ctx.moveTo(eyeX);

    // Eyes
    ctx.fillStyle = "black";
    ctx.beginPath();
    ctx.arc(x(eyeX), y(eyeY), eyeR, 0, 2 * Math.PI);
    ctx.fill();
    ctx.beginPath();
    ctx.arc(x(eyeX + eyeSeparation), y(eyeY), eyeR, 0, 2 * Math.PI);
    ctx.fill();

    // Ideas:
    // - Make the mouth a different color
    // - Different shapes (ellipse, rotation, ... a blobby path?)
    ctx.fillStyle = "black";
    ctx.beginPath();
    ctx.arc(x(mouthX), y(mouthY), mouthR, 0, 2 * Math.PI);
    ctx.fill();
}

class AvatarGen extends HTMLElement {
    connectedCallback() {
        this.addEventListener("click", (e) => {
            if (!e.target.matches("[data-action=generate]")) return;

            const canvas = this.querySelector("canvas");
            if (!canvas) {
                throw new Error("h-avatar-gen: couldn't find canvas");
            }

            drawAvatar(canvas);
            canvas.toBlob((blob) => {
                const url = URL.createObjectURL(blob);
            });
        });
    }
}
customElements.define("h-avatar-gen", AvatarGen);

class AvatarImg extends HTMLElement {
    connectedCallback() {
        const canvas = document.createElement("canvas");
        canvas.width = 256;
        canvas.height = 256;
        drawAvatar(canvas);
        canvas.toBlob((blob) => {
            const url = URL.createObjectURL(blob);
            const img = this.querySelector("img");
            img.src = url;
        });
    }
}
customElements.define("h-avatar-img", AvatarImg);
