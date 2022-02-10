import React, { useRef, useEffect } from "react";

export default function VideoStream({ src, height, width, inpChannel }) {
  const videoRef = useRef(null);

  useEffect(() => {
    if (!src) return;

    videoRef.current.srcObject = src;
  }, [src]);

  const sendMouseDown = (data) => {
    if (!inpChannel) return;

    inpChannel.send(
      JSON.stringify({
        type: "MOUSEDOWN",
        data: JSON.stringify(data),
      })
    );
  };

  const sendMouseUp = (data) => {
    if (!inpChannel) return;

    inpChannel.send(
      JSON.stringify({
        type: "MOUSEUP",
        data: JSON.stringify(data),
      })
    );
  };

  const sendMouseMove = (data) => {
    if (!inpChannel) return;

    inpChannel.send(
      JSON.stringify({
        type: "MOUSEMOVE",
        data: JSON.stringify(data),
      })
    );
  };

  const isLeftButton = (button) => {
    return button === 0 ? 1 : 0; // 1 is right button
  };

  const onMouseDown = (event) => {
    const boundRect = videoRef.current.getBoundingClientRect();
    sendMouseDown({
      isLeft: isLeftButton(event.button),
      x: event.clientX - boundRect.left,
      y: event.clientY - boundRect.top,
      width: boundRect.width,
      height: boundRect.height,
    });
  };

  const onMouseUp = (event) => {
    const boundRect = videoRef.current.getBoundingClientRect();
    sendMouseUp({
      isLeft: isLeftButton(event.button),
      x: event.clientX - boundRect.left,
      y: event.clientY - boundRect.top,
      width: boundRect.width,
      height: boundRect.height,
    });
  };

  const onMouseMove = (event) => {
    const boundRect = videoRef.current.getBoundingClientRect();
    sendMouseMove({
      isLeft: isLeftButton(event.button),
      x: event.clientX - boundRect.left,
      y: event.clientY - boundRect.top,
      width: boundRect.width,
      height: boundRect.height,
    });
  };

  return (
    <video
      height={height}
      width={width}
      ref={videoRef}
      autoPlay
      muted
      onMouseDown={onMouseDown}
      onMouseUp={onMouseUp}
      onMouseMove={onMouseMove}
    />
  );
}
