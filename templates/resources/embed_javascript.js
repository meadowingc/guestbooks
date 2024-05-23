(function () {
  var form = document.getElementById("guestbooks___guestbook-form");
  var messagesContainer = document.getElementById(
    "guestbooks___guestbook-messages-container"
  );

  form.addEventListener("submit", function (event) {
    event.preventDefault();

    var formData = new FormData(form);
    fetch(form.action, {
      method: "POST",
      body: formData,
    })
      .then(function (response) {
        if (!response.ok) {
          throw new Error("Network response was not ok");
        }
      })
      .then(function () {
        form.reset();
        guestbooks___loadMessages();
      })
      .catch(function (error) {
        console.error("Error:", error);
      });
  });

  function guestbooks___loadMessages() {
    var apiUrl =
      "https://guestbooks.meadowing.club/api/v1/get-guestbook-messages/{{.GuestbookID}}";
    fetch(apiUrl)
      .then(function (response) {
        return response.json();
      })
      .then(function (messages) {
        var messagesList = document.createElement("ul");
        messages.forEach(function (message) {
          var listItem = document.createElement("li");
          var messageContent;
          if (message.Website) {
            var link = document.createElement("a");
            link.href = message.Website;
            link.textContent = message.Name;
            messageContent = document.createTextNode(": " + message.Text);
            listItem.appendChild(link);
            listItem.appendChild(messageContent);
          } else {
            messageContent = document.createTextNode(
              message.Name + ": " + message.Text
            );
            listItem.appendChild(messageContent);
          }
          messagesList.appendChild(listItem);
        });
        messagesContainer.innerHTML = "";
        messagesContainer.appendChild(messagesList);
      })
      .catch(function (error) {
        console.error("Error fetching messages:", error);
      });
  }

  guestbooks___loadMessages();
})();
