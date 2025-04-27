class InPlaceElement extends HTMLElement {
    constructor() {
        super();
        this.handleSubmit = this.handleSubmit.bind(this);
    }
    connectedCallback() {
        this.addEventListener("submit", this.handleSubmit);
    }
    async handleSubmit(e) {
        const form = e.target;
        const container = form.closest("[data-in-place]");
        if (!container) {
            console.log("h-in-place: no [data-in-place] container found", e);
            return;
        }
        if (!container.id) {
            throw new Error(
                "h-in-place: container with [data-in-place] needs id",
                container
            );
        }
        e.preventDefault();
        const formData = new FormData(form, e.submitter);
        // TODO: error handling?
        const resp = await fetch(form.action, {
            method: form.method,
            body: formData,
        });
        const text = await resp.text();
        const parser = new DOMParser();
        const doc = parser.parseFromString(text, "text/html");
        const newDomNode = doc.getElementById(container.id);
        if (!newDomNode) {
            console.log(
                `h-in-place: could not find element with id ${container.id}; replacing body`
            );
            document.body.replaceWith(doc.body);
            return;
        }
        // TODO: could try to morph here
        console.log(`h-in-place: replacing ${container.id}`);
        container.replaceWith(newDomNode);
    }
}

customElements.define("h-in-place", InPlaceElement);
