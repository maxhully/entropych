const banner = `\
    ===================================================================
===========================================================================
======= THIS BROWSER TAB IS NOW UNDER THE CONTROL OF ENTROPYCH, INC =======
===========================================================================
    ===================================================================
`;
console.log(banner);

// <h-infinite-scroll>: ENTROPYCH, INC's
//
// Fetches the next page of items in a paginated list and adds them to the list in the
// current document.
//
// To use this element, you need:
//
// - A data-controls attribute with the id of the container to put the new items in
// - An <a data-rel="next"> child element whose href attribute is the URL to fetch for
//   the next page of items
//
// TODO: problem want a way to remove old items from the top of the page? although we
// could fetch a bajillion items and not exceed the
class InfiniteScrollElement extends HTMLElement {
    constructor() {
        super();
        this.handleIntersection = this.handleIntersection.bind(this);
        this.isFetching = false;
    }
    connectedCallback() {
        const a = this.querySelector("a[data-rel=next]");
        if (!a) {
            throw new Error("could not find a next link");
        }
        this.href = a.getAttribute("href");
        // It's probably possible to make this a module-level global that all the
        // <h-infinite-scroll> elements share
        const observer = new IntersectionObserver(this.handleIntersection);
        observer.observe(a);
    }

    async handleIntersection(entries, observer) {
        if (!entries) return;
        const isIntersecting = entries[entries.length - 1].isIntersecting;
        if (!isIntersecting) return;
        if (this.isFetching) return;
        this.isFetching = true;

        let newDoc;
        try {
            const resp = await fetch(this.href);
            const html = await resp.text();
            const parser = new DOMParser();
            newDoc = parser.parseFromString(html, "text/html");
        } catch (e) {
            console.error(e);
            this.isFetching = false;
            return;
        }
        observer.disconnect();

        const controlsId = this.dataset.controls;
        const container = document.getElementById(controlsId);
        const fetchedContainer = newDoc.getElementById(controlsId);
        container.append(...fetchedContainer.children);

        // Is there anything more I should do here to avoid leaking this element?
        this.remove();
    }
}

customElements.define("h-infinite-scroll", InfiniteScrollElement);
