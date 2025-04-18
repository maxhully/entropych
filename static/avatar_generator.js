// Maybe I should make a go implementation with this library:
// https://github.com/tdewolff/canvas

function randomEllipseSector(rxMin, rxMax, ryMin, ryMax) {
    // Ellipse types
    const CIRCLE = 0;
    const SMILE = 1;
    const FROWN = 2;

    const rx = rxMin + Math.random() * (rxMax - rxMin);
    const ry = ryMin + Math.random() * (ryMax - ryMin);

    let rotationEnd;
    let ccw = false;
    const mouthType = Math.floor(Math.random() * 3);
    if (mouthType == CIRCLE) {
        rotationEnd = Math.PI * 2;
    } else if (mouthType == SMILE) {
        rotationEnd = Math.PI;
    } else if (mouthType == FROWN) {
        rotationEnd = Math.PI;
        ccw = true;
    }
    const rotationStart = -Math.PI / 2 + Math.PI * Math.random();
    return {
        rx,
        ry,
        rotationStart,
        rotationEnd,
        ccw,
    };
}

function randomColor() {
    const h = Math.round(Math.random() * 360);
    const s = Math.round(Math.random() * 20) + 80;
    const l = Math.round(Math.random() * 70) + 20;
    return { h, s, l };
}

function colorToString({ h, s, l }) {
    console.log({ h, s, l });
    return `hsl(${h}, ${s}%, ${l}%)`;
}

/**
 * Generates a random avatar on the given canvas.
 *
 * @param {HTMLCanvasElement} canvas
 */
function drawAvatar(canvas) {
    const ctx = canvas.getContext("2d");
    ctx.reset();

    const bgColor = randomColor();
    const fgColor = { h: (bgColor.h + 180) % 360, s: 95, l: bgColor.l };
    fgColor.l = Math.round(100 * Math.pow(fgColor.l / 100, 3));
    bgColor.l = Math.round(100 * Math.pow(bgColor.l / 100, 1 / 3));

    // All of these measurements are relative to the canvas width/height.
    const eye = randomEllipseSector(0.025, 0.2, 0.025, 0.15);
    eye.rotationStart = 0;
    const eyeSeparation = eye.rx + (1.0 - eye.rx) * Math.random();
    // I could tweak the eyeX / mouthX so that the face always looks like it's facing to
    // the right
    // TODO: I want the eye/mouth X and Y to stay somewhat central
    const eyeX = Math.random() * (1.0 - eyeSeparation);
    const eyeY = Math.random() * 0.8;

    const mouth = randomEllipseSector(0.025, 0.6, 0.025, 0.4);
    // TODO: This doesn't account for the mouth/eye possibly being a half-moon
    const ySpace = eyeY + eye.ry + mouth.ry;
    const mouthY = ySpace + Math.random() * (1.0 - ySpace);
    const mouthX = Math.random();

    const w = canvas.width;
    const h = canvas.height;
    function x(xCoord01) {
        return w * xCoord01;
    }
    function y(yCoord01) {
        return h * yCoord01;
    }

    ctx.fillStyle = colorToString(bgColor);
    ctx.fillRect(0, 0, w, h);

    // Eyes
    ctx.fillStyle = colorToString(fgColor);
    ctx.beginPath();
    ctx.ellipse(
        x(eyeX),
        y(eyeY),
        x(eye.rx),
        y(eye.ry),
        0,
        0,
        eye.rotationEnd,
        eye.ccw
    );
    ctx.fill();
    ctx.beginPath();
    ctx.ellipse(
        x(eyeX + eyeSeparation),
        y(eyeY),
        x(eye.rx),
        y(eye.ry),
        0,
        0,
        eye.rotationEnd,
        eye.ccw
    );
    ctx.fill();

    // Ideas:
    // - Make the mouth a different color
    // - Different shapes (ellipse, rotation, ... a blobby path?)
    ctx.fillStyle = colorToString(fgColor);
    ctx.beginPath();
    ctx.ellipse(
        x(mouthX),
        y(mouthY),
        w * mouth.rx,
        h * mouth.ry,
        0, // I think rotating this would be good
        mouth.rotationStart,
        mouth.rotationEnd,
        mouth.ccw
    );
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
